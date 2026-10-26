package proxy

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/Bliod-Cook/RSSHuBalance/internal/stats"
)

const defaultCooldown = 60 * time.Second

// verdict is the classification of an upstream response.
type verdict struct {
	outcome stats.Outcome
	// notFound marks a missing route, so that all-404 attempts answer 404.
	notFound bool
	cooldown time.Duration
}

func classify(status int, header http.Header, body []byte, now time.Time) verdict {
	switch {
	case status == http.StatusTooManyRequests:
		return verdict{outcome: stats.RateLimited, cooldown: retryAfter(header.Get("Retry-After"), now)}
	case status >= 200 && status < 300 && isFeed(body):
		return verdict{outcome: stats.Success}
	case status == http.StatusForbidden || status == http.StatusBadGateway || status == http.StatusGatewayTimeout:
		return verdict{outcome: stats.InstanceFailure}
	case status == http.StatusNotFound:
		return verdict{outcome: stats.RouteFailure, notFound: true}
	case fromRSSHub(header, body):
		// The instance is up; the route failed or did not produce a feed.
		// Older RSSHub versions answer a missing route with 503.
		return verdict{outcome: stats.RouteFailure, notFound: bytes.Contains(body, []byte("NotFoundError:"))}
	}
	return verdict{outcome: stats.InstanceFailure}
}

// isFeed reports whether body is JSON or XML with an rss, feed or RDF root.
func isFeed(body []byte) bool {
	if json.Valid(body) {
		return true
	}
	d := xml.NewDecoder(bytes.NewReader(body))
	// Only the ASCII root element name is needed, so any declared charset will do.
	d.CharsetReader = func(_ string, r io.Reader) (io.Reader, error) { return r, nil }
	for {
		tok, err := d.Token()
		if err != nil {
			return false
		}
		if el, ok := tok.(xml.StartElement); ok {
			switch el.Name.Local {
			case "rss", "feed", "RDF":
				return true
			}
			return false
		}
	}
}

// fromRSSHub recognizes responses produced by RSSHub itself: its header
// middleware sets Access-Control-Allow-Methods on every response, and its
// error page and the ?format=json variant have a fixed shape.
func fromRSSHub(header http.Header, body []byte) bool {
	if header.Get("Access-Control-Allow-Methods") == "GET" || bytes.Contains(body, []byte("Looks like something went wrong")) {
		return true
	}
	var v struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	return json.Unmarshal(body, &v) == nil && v.Error != nil
}

func retryAfter(v string, now time.Time) time.Duration {
	if s, err := strconv.Atoi(v); err == nil && s >= 0 {
		return time.Duration(s) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		return t.Sub(now)
	}
	return defaultCooldown
}
