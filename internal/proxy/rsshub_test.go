package proxy

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Bliod-Cook/RSSHuBalance/internal/stats"
)

// Tests against real RSSHub instances, e.g.
// RSSHUB_URLS=https://rss.materium.io,https://rss.mrbiosci.com go test ./...
func rsshubURLs(t *testing.T) []string {
	t.Helper()
	v := os.Getenv("RSSHUB_URLS")
	if v == "" {
		t.Skip("RSSHUB_URLS not set")
	}
	return strings.Split(v, ",")
}

const missingRoute = "/rsshubalance-missing-namespace/x"

func TestRealRSSHubClassification(t *testing.T) {
	urls := rsshubURLs(t)
	h := newHandler(newPool(t), stats.New(), 30*time.Second)
	cases := []struct {
		path string
		want verdict
	}{
		{"/test/1", verdict{outcome: stats.Success}},
		{"/test/error", verdict{outcome: stats.RouteFailure}},
		{"/test/error?format=json", verdict{outcome: stats.RouteFailure}},
		{missingRoute, verdict{outcome: stats.RouteFailure, notFound: true}},
	}
	for _, u := range urls {
		for _, c := range cases {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			res := h.fetch(ctx, u+c.path)
			cancel()
			if res.verdict != c.want {
				t.Errorf("%s%s: got %+v (status %d), want %+v", u, c.path, res.verdict, res.status, c.want)
			}
		}
	}
}

func TestRealRSSHubTimeout(t *testing.T) {
	urls := rsshubURLs(t)
	h := newHandler(newPool(t), stats.New(), 2*time.Second)
	// A fresh path bypasses RSSHub's route cache.
	path := "/test/slow4/" + strconv.FormatInt(time.Now().UnixNano(), 36)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if res := h.fetch(ctx, urls[0]+path); res.outcome != stats.Timeout {
		t.Fatalf("got %+v, want Timeout", res.verdict)
	}
}

func TestRealRSSHubThroughHandler(t *testing.T) {
	urls := rsshubURLs(t)
	closed := closedURL(t)
	s := stats.New()
	warm(s, closed, "/test/1", 10*time.Millisecond)
	h := newHandler(newPool(t, instances(append(urls, closed)...)...), s, 30*time.Second)

	if rec := get(h, "/test/1"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "<rss") {
		t.Errorf("/test/1: status %d, body %.200s", rec.Code, rec.Body.String())
	}

	h = newHandler(newPool(t, instances(urls...)...), stats.New(), 30*time.Second)
	if rec := get(h, missingRoute); rec.Code != 404 {
		t.Errorf("%s: status %d, want 404", missingRoute, rec.Code)
	}
}
