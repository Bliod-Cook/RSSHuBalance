package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestLoadConfigDefaults(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{"RSSHUBALANCE_ADMIN_TOKEN": "secret"}))
	if err != nil {
		t.Fatal(err)
	}
	want := config{
		listen:          ":1200",
		upstreamTimeout: 60 * time.Second,
		maxAttempts:     3,
		adminToken:      "secret",
		stateFile:       "instances.json",
	}
	if cfg != want {
		t.Fatalf("got %+v, want %+v", cfg, want)
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	cfg, err := loadConfig(env(map[string]string{
		"RSSHUBALANCE_LISTEN":           "127.0.0.1:8080",
		"RSSHUBALANCE_UPSTREAM_TIMEOUT": "30s",
		"RSSHUBALANCE_MAX_ATTEMPTS":     "5",
		"RSSHUBALANCE_ADMIN_TOKEN":      "secret",
		"RSSHUBALANCE_STATE_FILE":       "/data/instances.json",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := config{
		listen:          "127.0.0.1:8080",
		upstreamTimeout: 30 * time.Second,
		maxAttempts:     5,
		adminToken:      "secret",
		stateFile:       "/data/instances.json",
	}
	if cfg != want {
		t.Fatalf("got %+v, want %+v", cfg, want)
	}
}

func TestRouting(t *testing.T) {
	marker := func(name string) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(name + " " + r.URL.Path))
		})
	}
	h := newHandler(marker("proxy"), marker("admin"))
	for target, want := range map[string]string{
		"/healthz":                     "ok",
		"/admin/instances":             "admin /admin/instances",
		"/transform/https://a.example": "proxy /transform/https://a.example",
		"/x/../y":                      "proxy /x/../y",
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
		if rec.Code != 200 || rec.Body.String() != want {
			t.Errorf("%s: %d %q, want %q", target, rec.Code, rec.Body.String(), want)
		}
	}
}

func TestRootPageIsServedLocally(t *testing.T) {
	fail := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("root must not be forwarded") })
	rec := httptest.NewRecorder()
	newHandler(fail, fail).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	body := rec.Body.String()
	if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/html") || !strings.Contains(body, "RSSHuBalance") {
		t.Fatalf("got %d %q %q", rec.Code, rec.Header().Get("Content-Type"), body)
	}
	if !strings.Contains(body, `rel="icon"`) {
		t.Error("page should declare an icon so browsers do not request /favicon.ico")
	}
}

func TestLoadConfigErrors(t *testing.T) {
	for name, m := range map[string]map[string]string{
		"missing token": {},
		"bad timeout":   {"RSSHUBALANCE_ADMIN_TOKEN": "x", "RSSHUBALANCE_UPSTREAM_TIMEOUT": "soon"},
		"zero attempts": {"RSSHUBALANCE_ADMIN_TOKEN": "x", "RSSHUBALANCE_MAX_ATTEMPTS": "0"},
		"bad attempts":  {"RSSHUBALANCE_ADMIN_TOKEN": "x", "RSSHUBALANCE_MAX_ATTEMPTS": "many"},
	} {
		if _, err := loadConfig(env(m)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
