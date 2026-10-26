package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Bliod-Cook/RSSHuBalance/internal/pool"
	"github.com/Bliod-Cook/RSSHuBalance/internal/stats"
)

const token = "secret"

func newAPI(t *testing.T) (http.Handler, *pool.Pool, *stats.Stats) {
	t.Helper()
	p, err := pool.Load(filepath.Join(t.TempDir(), "instances.json"))
	if err != nil {
		t.Fatal(err)
	}
	s := stats.New()
	return New(token, p, s), p, s
}

func do(h http.Handler, method, target, body, auth string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestRequiresBearerToken(t *testing.T) {
	h, _, _ := newAPI(t)
	for _, auth := range []string{"", "Bearer wrong", token} {
		if rec := do(h, "GET", "/admin/instances", "", auth); rec.Code != 401 {
			t.Errorf("auth %q: status %d, want 401", auth, rec.Code)
		}
	}
	if rec := do(h, "GET", "/admin/instances", "", "Bearer "+token); rec.Code != 200 {
		t.Errorf("valid token: status %d", rec.Code)
	}
}

func TestManageInstances(t *testing.T) {
	h, _, _ := newAPI(t)
	auth := "Bearer " + token
	steps := []struct {
		method, target, body string
		status               int
		response             string
	}{
		{"PUT", "/admin/instances", `{"url":"https://a.example/"}`, 200, `{"url":"https://a.example","max_concurrency":4}`},
		{"PUT", "/admin/instances", `{"url":"ftp://a.example"}`, 400, ""},
		{"PUT", "/admin/instances", `{`, 400, ""},
		{"GET", "/admin/instances", "", 200, `[{"url":"https://a.example","max_concurrency":4}]`},
		{"DELETE", "/admin/instances?url=https://a.example", "", 204, ""},
		{"DELETE", "/admin/instances?url=https://a.example", "", 404, ""},
		{"GET", "/admin/instances", "", 200, `[]`},
	}
	for _, s := range steps {
		rec := do(h, s.method, s.target, s.body, auth)
		if rec.Code != s.status {
			t.Fatalf("%s %s %s: status %d, want %d", s.method, s.target, s.body, rec.Code, s.status)
		}
		if s.response != "" && strings.TrimSpace(rec.Body.String()) != s.response {
			t.Fatalf("%s %s: body %s, want %s", s.method, s.target, rec.Body.String(), s.response)
		}
	}
}

func TestStats(t *testing.T) {
	h, p, s := newAPI(t)
	if _, err := p.Upsert(pool.Instance{URL: "https://a.example"}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	s.Record("https://a.example", "/x/feed", stats.Success, now.Add(-200*time.Millisecond), now)

	rec := do(h, "GET", "/admin/stats", "", "Bearer "+token)
	var got []struct {
		URL string `json:"url"`
		stats.Snapshot
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != 200 {
		t.Fatalf("status %d, body %s, err %v", rec.Code, rec.Body.String(), err)
	}
	if len(got) != 1 || got[0].URL != "https://a.example" || got[0].PUp <= 0.5 || got[0].Namespaces["x"].SuccessLatencyMs == 0 {
		t.Fatalf("unexpected stats %+v", got)
	}
}
