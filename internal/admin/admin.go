// Package admin serves the bearer-token protected management API.
package admin

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/Bliod-Cook/RSSHuBalance/internal/pool"
	"github.com/Bliod-Cook/RSSHuBalance/internal/stats"
)

type api struct {
	pool  *pool.Pool
	stats *stats.Stats
}

func New(token string, p *pool.Pool, s *stats.Stats) http.Handler {
	a := &api{pool: p, stats: s}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /admin/instances", a.listInstances)
	mux.HandleFunc("PUT /admin/instances", a.putInstance)
	mux.HandleFunc("DELETE /admin/instances", a.deleteInstance)
	mux.HandleFunc("GET /admin/stats", a.listStats)

	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (a *api) listInstances(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, a.pool.List())
}

func (a *api) putInstance(w http.ResponseWriter, r *http.Request) {
	var in pool.Instance
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	saved, err := a.pool.Upsert(in)
	switch {
	case errors.Is(err, pool.ErrInvalid):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	default:
		writeJSON(w, saved)
	}
}

func (a *api) deleteInstance(w http.ResponseWriter, r *http.Request) {
	ok, err := a.pool.Delete(r.URL.Query().Get("url"))
	switch {
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	case !ok:
		http.Error(w, "instance not found", http.StatusNotFound)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

type instanceStats struct {
	URL string `json:"url"`
	stats.Snapshot
}

func (a *api) listStats(w http.ResponseWriter, _ *http.Request) {
	now := time.Now()
	list := a.pool.List()
	out := make([]instanceStats, len(list))
	for i, in := range list {
		out[i] = instanceStats{URL: in.URL, Snapshot: a.stats.Snapshot(in.URL, now)}
	}
	writeJSON(w, out)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
