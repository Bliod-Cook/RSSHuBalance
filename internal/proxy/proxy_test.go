package proxy

import (
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Bliod-Cook/RSSHuBalance/internal/pool"
	"github.com/Bliod-Cook/RSSHuBalance/internal/stats"
)

func newPool(t *testing.T, instances ...pool.Instance) *pool.Pool {
	t.Helper()
	p, err := pool.Load(filepath.Join(t.TempDir(), "instances.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range instances {
		if _, err := p.Upsert(in); err != nil {
			t.Fatal(err)
		}
	}
	return p
}

func instances(urls ...string) []pool.Instance {
	list := make([]pool.Instance, len(urls))
	for i, u := range urls {
		list[i] = pool.Instance{URL: u}
	}
	return list
}

func newHandler(p *pool.Pool, s *stats.Stats, timeout time.Duration) *Handler {
	return New(Config{Pool: p, Stats: s, UserAgent: "RSSHuBalance/test", UpstreamTimeout: timeout, MaxAttempts: 3})
}

func get(h http.Handler, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

// upstream starts a server and counts its requests.
func upstream(t *testing.T, fn http.HandlerFunc) (string, *atomic.Int32) {
	t.Helper()
	hits := &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		fn(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, hits
}

func feed(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Write([]byte(rssBody))
}

func reply(status int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte(body))
	}
}

// slow delays the reply unless the request is cancelled first.
func slow(d time.Duration, fn http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(d):
			fn(w, r)
		case <-r.Context().Done():
		}
	}
}

func closedURL(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
	return "http://" + ln.Addr().String()
}

// warm records fast successes so the instance ranks first.
func warm(s *stats.Stats, u, path string, latency time.Duration) {
	now := time.Now()
	for range 20 {
		s.Record(u, path, stats.Success, now.Add(-latency), now)
	}
}

func eventually(t *testing.T, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); !cond(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
	}
}

func TestForwardsRequestVerbatim(t *testing.T) {
	const target = "/transform/https%3A%2F%2Fa.example%2Fx?limit=5&filter=a%20b"
	var gotURI, gotUA string
	u, _ := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		gotURI, gotUA = r.RequestURI, r.UserAgent()
		w.Header().Set("Content-Type", "application/rss+xml; charset=utf-8")
		w.Header().Set("X-Upstream", "1")
		w.Write([]byte(rssBody))
	})
	h := newHandler(newPool(t, pool.Instance{URL: u + "/prefix/"}), stats.New(), 5*time.Second)

	rec := get(h, target)
	if rec.Code != 200 || rec.Body.String() != rssBody {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/rss+xml; charset=utf-8" {
		t.Errorf("content type %q", ct)
	}
	if rec.Header().Get("X-Upstream") != "" {
		t.Error("only Content-Type should be passed through")
	}
	if gotURI != "/prefix"+target || gotUA != "RSSHuBalance/test" {
		t.Errorf("upstream saw %q with UA %q", gotURI, gotUA)
	}
}

func TestFallsBackToHealthyInstance(t *testing.T) {
	const path = "/x/feed"
	bad, badHits := upstream(t, reply(502, "Bad Gateway"))
	closed := closedURL(t)
	good, goodHits := upstream(t, feed)
	s := stats.New()
	warm(s, bad, path, 10*time.Millisecond)
	warm(s, closed, path, 10*time.Millisecond)

	rec := get(newHandler(newPool(t, instances(bad, closed, good)...), s, 5*time.Second), path)
	if rec.Code != 200 || badHits.Load() != 1 || goodHits.Load() != 1 {
		t.Fatalf("status %d, bad hits %d, good hits %d", rec.Code, badHits.Load(), goodHits.Load())
	}
}

func TestStatusWhenAllAttemptsFail(t *testing.T) {
	serve := func(fn http.HandlerFunc) string { u, _ := upstream(t, fn); return u }
	notFound := serve(reply(404, "Not Found"))
	rsshubNotFound := serve(reply(503, rsshubError("NotFoundError: ")))
	badGateway := serve(reply(502, "Bad Gateway"))
	cases := []struct {
		name      string
		instances []string
		want      int
	}{
		{"404 and rsshub not found page", []string{notFound, rsshubNotFound}, 404},
		{"404 and 502", []string{notFound, badGateway}, 502},
		{"no instances", nil, 502},
	}
	for _, c := range cases {
		h := newHandler(newPool(t, instances(c.instances...)...), stats.New(), 5*time.Second)
		if rec := get(h, "/x/feed"); rec.Code != c.want {
			t.Errorf("%s: got %d, want %d", c.name, rec.Code, c.want)
		}
	}
}

func TestHedgesSlowAttempt(t *testing.T) {
	const path = "/x/feed"
	slowURL, slowHits := upstream(t, slow(time.Second, feed))
	fastURL, fastHits := upstream(t, feed)
	s := stats.New()
	warm(s, slowURL, path, 50*time.Millisecond)

	start := time.Now()
	rec := get(newHandler(newPool(t, instances(slowURL, fastURL)...), s, 5*time.Second), path)
	if elapsed := time.Since(start); rec.Code != 200 || elapsed > 500*time.Millisecond {
		t.Fatalf("status %d after %v", rec.Code, elapsed)
	}
	if slowHits.Load() != 1 || fastHits.Load() != 1 {
		t.Fatalf("slow hits %d, fast hits %d", slowHits.Load(), fastHits.Load())
	}
	// The slow attempt keeps running and is recorded as the 21st success.
	eventually(t, func() bool { return s.Snapshot(slowURL, time.Now()).PUp > 21.5/22.5 })
}

func TestQueuesWhenInstancesFull(t *testing.T) {
	var cur, peak atomic.Int32
	u, _ := upstream(t, func(w http.ResponseWriter, r *http.Request) {
		n := cur.Add(1)
		defer cur.Add(-1)
		if n > peak.Load() {
			peak.Store(n)
		}
		time.Sleep(100 * time.Millisecond)
		feed(w, r)
	})
	h := newHandler(newPool(t, pool.Instance{URL: u, MaxConcurrency: 1}), stats.New(), 5*time.Second)

	var wg sync.WaitGroup
	codes := make([]int, 3)
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			codes[i] = get(h, "/x/feed").Code
		}()
	}
	wg.Wait()
	for i, code := range codes {
		if code != 200 {
			t.Errorf("request %d: status %d", i, code)
		}
	}
	if peak.Load() != 1 {
		t.Errorf("peak upstream concurrency %d, want 1", peak.Load())
	}
}

func TestRateLimitedInstanceCoolsDown(t *testing.T) {
	const path = "/x/feed"
	limited, limitedHits := upstream(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(429)
	})
	ok, _ := upstream(t, feed)
	s := stats.New()
	warm(s, limited, path, 10*time.Millisecond)
	h := newHandler(newPool(t, instances(limited, ok)...), s, 5*time.Second)

	for i := range 2 {
		if rec := get(h, path); rec.Code != 200 {
			t.Fatalf("request %d: status %d", i, rec.Code)
		}
	}
	if limitedHits.Load() != 1 {
		t.Fatalf("rate-limited instance hit %d times, want 1", limitedHits.Load())
	}
}

func TestTimeout(t *testing.T) {
	const path = "/x/feed"
	u, _ := upstream(t, slow(2*time.Second, feed))
	s := stats.New()
	h := newHandler(newPool(t, instances(u)...), s, 200*time.Millisecond)

	start := time.Now()
	if rec := get(h, path); rec.Code != 502 || time.Since(start) > time.Second {
		t.Fatalf("status %d after %v", rec.Code, time.Since(start))
	}
	eventually(t, func() bool {
		snap := s.Snapshot(u, time.Now())
		return snap.PUp < 0.5 && snap.PRoute < 0.5
	})
}
