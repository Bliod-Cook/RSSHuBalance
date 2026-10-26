package main

import (
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Bliod-Cook/RSSHuBalance/internal/admin"
	"github.com/Bliod-Cook/RSSHuBalance/internal/pool"
	"github.com/Bliod-Cook/RSSHuBalance/internal/proxy"
	"github.com/Bliod-Cook/RSSHuBalance/internal/stats"
)

var version = "dev"

type config struct {
	listen          string
	upstreamTimeout time.Duration
	maxAttempts     int
	adminToken      string
	stateFile       string
}

func loadConfig(getenv func(string) string) (config, error) {
	cfg := config{
		listen:          ":1200",
		upstreamTimeout: 60 * time.Second,
		maxAttempts:     3,
		adminToken:      getenv("RSSHUBALANCE_ADMIN_TOKEN"),
		stateFile:       "instances.json",
	}
	if cfg.adminToken == "" {
		return cfg, errors.New("RSSHUBALANCE_ADMIN_TOKEN is required")
	}
	if v := getenv("RSSHUBALANCE_LISTEN"); v != "" {
		cfg.listen = v
	}
	if v := getenv("RSSHUBALANCE_STATE_FILE"); v != "" {
		cfg.stateFile = v
	}
	if v := getenv("RSSHUBALANCE_UPSTREAM_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return cfg, fmt.Errorf("invalid RSSHUBALANCE_UPSTREAM_TIMEOUT %q", v)
		}
		cfg.upstreamTimeout = d
	}
	if v := getenv("RSSHUBALANCE_MAX_ATTEMPTS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			return cfg, fmt.Errorf("invalid RSSHUBALANCE_MAX_ATTEMPTS %q", v)
		}
		cfg.maxAttempts = n
	}
	return cfg, nil
}

// rootPage declares an empty icon so browsers do not request /favicon.ico,
// which would be forwarded and counted as an instance failure.
const rootPage = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>RSSHuBalance</title><link rel="icon" href="data:,"></head>
<body><h1>RSSHuBalance</h1><p>RSSHub routes on this host are forwarded to the instance pool.</p></body>
</html>
`

// newHandler dispatches without http.ServeMux, which would redirect
// non-canonical paths instead of forwarding them verbatim.
func newHandler(proxyHandler, adminHandler http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(rootPage))
		case r.URL.Path == "/healthz":
			w.Write([]byte("ok"))
		case strings.HasPrefix(r.URL.Path, "/admin/"):
			adminHandler.ServeHTTP(w, r)
		default:
			proxyHandler.ServeHTTP(w, r)
		}
	})
}

func main() {
	cfg, err := loadConfig(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	p, err := pool.Load(cfg.stateFile)
	if err != nil {
		log.Fatal(err)
	}
	s := stats.New()
	proxyHandler := proxy.New(proxy.Config{
		Pool:            p,
		Stats:           s,
		UserAgent:       "RSSHuBalance/" + version,
		UpstreamTimeout: cfg.upstreamTimeout,
		MaxAttempts:     cfg.maxAttempts,
	})
	srv := &http.Server{
		Addr:              cfg.listen,
		Handler:           newHandler(proxyHandler, admin.New(cfg.adminToken, p, s)),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("RSSHuBalance %s listening on %s with %d instances", version, cfg.listen, len(p.List()))
	log.Fatal(srv.ListenAndServe())
}
