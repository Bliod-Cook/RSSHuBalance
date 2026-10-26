// Package proxy forwards RSSHub requests to the best-ranked instances.
package proxy

import (
	"context"
	"io"
	"math/rand/v2"
	"net/http"
	"time"

	"github.com/Bliod-Cook/RSSHuBalance/internal/pool"
	"github.com/Bliod-Cook/RSSHuBalance/internal/stats"
)

const maxInflight = 2

type Config struct {
	Pool      *pool.Pool
	Stats     *stats.Stats
	UserAgent string
	// UpstreamTimeout bounds each attempt and the client's wait.
	UpstreamTimeout time.Duration
	MaxAttempts     int
}

type Handler struct {
	cfg    Config
	client *http.Client
}

func New(cfg Config) *Handler {
	return &Handler{cfg: cfg, client: &http.Client{}}
}

type attemptResult struct {
	verdict
	status      int
	contentType string
	body        []byte
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	target := r.URL.EscapedPath()
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	now := time.Now()
	rng := rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))
	order := rank(h.cfg.Pool.Available(now), r.URL.Path, h.cfg.Stats, now, rng)

	res, status := h.schedule(r.Context(), order, r.URL.Path, target)
	switch {
	case res != nil:
		if res.contentType != "" {
			w.Header().Set("Content-Type", res.contentType)
		}
		w.WriteHeader(res.status)
		w.Write(res.body)
	case status != 0:
		http.Error(w, http.StatusText(status), status)
	}
}

// schedule tries instances in order until one succeeds. The next attempt
// starts when one fails, or in parallel once the latest attempt has run past
// its P90 success latency, with at most maxInflight attempts in flight. It
// returns the successful result, or nil with the status to answer (0 when the
// client has gone).
func (h *Handler) schedule(ctx context.Context, order []string, path, target string) (*attemptResult, int) {
	deadline := time.NewTimer(h.cfg.UpstreamTimeout)
	defer deadline.Stop()
	results := make(chan attemptResult, h.cfg.MaxAttempts)
	tried := make(map[string]bool, len(order))
	var (
		inflight, launched int
		want               = true
		exhausted          bool
		all404             = true
		hedge              <-chan time.Time
	)
	for {
		var slotFreed <-chan struct{}
		if want && inflight < maxInflight && launched < h.cfg.MaxAttempts {
			changed := h.cfg.Pool.Changed()
			switch u, state := h.acquireNext(order, tried); state {
			case pool.Acquired:
				tried[u] = true
				inflight++
				launched++
				want = false
				go h.attempt(u, path, target, results)
				hedge = nil
				if d, ok := h.cfg.Stats.HedgeDelay(u, path); ok {
					hedge = time.After(d)
				}
			case pool.Full:
				slotFreed = changed
			default:
				exhausted = true
				want = false
			}
		}
		if inflight == 0 && (exhausted || launched >= h.cfg.MaxAttempts) {
			if all404 && launched > 0 {
				return nil, http.StatusNotFound
			}
			return nil, http.StatusBadGateway
		}

		select {
		case res := <-results:
			inflight--
			if res.outcome == stats.Success {
				return &res, res.status
			}
			all404 = all404 && res.notFound
			want = true
		case <-hedge:
			hedge = nil
			want = true
		case <-slotFreed:
		case <-deadline.C:
			return nil, http.StatusBadGateway
		case <-ctx.Done():
			return nil, 0
		}
	}
}

// acquireNext takes a slot on the best untried instance. Full means every
// untried instance still available is at capacity.
func (h *Handler) acquireNext(order []string, tried map[string]bool) (string, pool.Acquire) {
	state := pool.Unavailable
	now := time.Now()
	for _, u := range order {
		if tried[u] {
			continue
		}
		switch h.cfg.Pool.TryAcquire(u, now) {
		case pool.Acquired:
			return u, pool.Acquired
		case pool.Full:
			state = pool.Full
		}
	}
	return "", state
}

// attempt runs detached from the client request, so attempts still running
// after the response is sent continue up to the upstream timeout and update
// the statistics.
func (h *Handler) attempt(instanceURL, path, target string, results chan<- attemptResult) {
	defer h.cfg.Pool.Release(instanceURL)
	ctx, cancel := context.WithTimeout(context.Background(), h.cfg.UpstreamTimeout)
	defer cancel()

	start := time.Now()
	res := h.fetch(ctx, instanceURL+target)
	end := time.Now()
	if res.outcome == stats.RateLimited {
		h.cfg.Pool.Cooldown(instanceURL, end.Add(res.cooldown))
	}
	h.cfg.Stats.Record(instanceURL, path, res.outcome, start, end)
	results <- res
}

func (h *Handler) fetch(ctx context.Context, url string) attemptResult {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return transportFailure(ctx)
	}
	req.Header.Set("User-Agent", h.cfg.UserAgent)
	resp, err := h.client.Do(req)
	if err != nil {
		return transportFailure(ctx)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return transportFailure(ctx)
	}
	return attemptResult{
		verdict:     classify(resp.StatusCode, resp.Header, body, time.Now()),
		status:      resp.StatusCode,
		contentType: resp.Header.Get("Content-Type"),
		body:        body,
	}
}

func transportFailure(ctx context.Context) attemptResult {
	if ctx.Err() != nil {
		return attemptResult{verdict: verdict{outcome: stats.Timeout}}
	}
	return attemptResult{verdict: verdict{outcome: stats.InstanceFailure}}
}
