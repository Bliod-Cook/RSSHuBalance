package proxy

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/Bliod-Cook/RSSHuBalance/internal/stats"
)

const (
	rssBody    = `<?xml version="1.0" encoding="UTF-8"?><rss version="2.0"><channel><title>t</title><item><title>i</title></item></channel></rss>`
	rsshubPage = `<html><head><title>Welcome to RSSHub!</title></head><body><h1 class="text-4xl font-bold">Looks like something went wrong</h1><code class="details">%s</code></body></html>`
	cfPage     = `<!DOCTYPE html><html lang="en-US"><head><title>Just a moment...</title></head><body>Checking your browser</body></html>`
)

func rsshubError(message string) string {
	return fmt.Sprintf(rsshubPage, message)
}

func TestClassify(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name       string
		status     int
		retryAfter string
		body       string
		want       verdict
	}{
		{"rss", 200, "", rssBody, verdict{outcome: stats.Success}},
		{"empty rss", 200, "", `<rss version="2.0"><channel><title>t</title></channel></rss>`, verdict{outcome: stats.Success}},
		{"atom", 200, "", `<?xml version="1.0"?><feed xmlns="http://www.w3.org/2005/Atom"></feed>`, verdict{outcome: stats.Success}},
		{"rdf", 200, "", `<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#"></rdf:RDF>`, verdict{outcome: stats.Success}},
		{"non-utf8 declaration", 200, "", `<?xml version="1.0" encoding="GBK"?><rss version="2.0"></rss>`, verdict{outcome: stats.Success}},
		{"json feed", 200, "", `{"version":"https://jsonfeed.org/version/1.1","items":[]}`, verdict{outcome: stats.Success}},
		{"2xx html", 200, "", cfPage, verdict{outcome: stats.InstanceFailure}},
		{"2xx empty", 200, "", "", verdict{outcome: stats.InstanceFailure}},
		{"403 plain", 403, "", "Due to cost considerations, we will gradually restrict access", verdict{outcome: stats.InstanceFailure}},
		{"403 rsshub", 403, "", rsshubError("RejectError: Authentication failed"), verdict{outcome: stats.InstanceFailure}},
		{"502", 502, "", "Bad Gateway", verdict{outcome: stats.InstanceFailure}},
		{"504 rsshub", 504, "", rsshubError("Error: x"), verdict{outcome: stats.InstanceFailure}},
		{"404", 404, "", "Not Found", verdict{outcome: stats.RouteFailure, notFound: true}},
		{"503 rsshub", 503, "", rsshubError("Error: Error test"), verdict{outcome: stats.RouteFailure}},
		{"503 rsshub not found", 503, "", rsshubError("NotFoundError: "), verdict{outcome: stats.RouteFailure, notFound: true}},
		{"503 rsshub json", 503, "", `{"error":{"message":"Error test"}}`, verdict{outcome: stats.RouteFailure}},
		{"503 non-rsshub", 503, "", cfPage, verdict{outcome: stats.InstanceFailure}},
		{"500 plain", 500, "", "Internal Server Error", verdict{outcome: stats.InstanceFailure}},
		{"401", 401, "", "Unauthorized", verdict{outcome: stats.InstanceFailure}},
		{"429 seconds", 429, "120", "", verdict{outcome: stats.RateLimited, cooldown: 120 * time.Second}},
		{"429 date", 429, now.Add(30 * time.Second).Format(http.TimeFormat), "", verdict{outcome: stats.RateLimited, cooldown: 30 * time.Second}},
		{"429 default", 429, "", "", verdict{outcome: stats.RateLimited, cooldown: 60 * time.Second}},
	}
	for _, c := range cases {
		header := http.Header{}
		if c.retryAfter != "" {
			header.Set("Retry-After", c.retryAfter)
		}
		if got := classify(c.status, header, []byte(c.body), now); got != c.want {
			t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
		}
	}
}
