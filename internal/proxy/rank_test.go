package proxy

import (
	"math/rand/v2"
	"slices"
	"testing"
	"time"

	"github.com/Bliod-Cook/RSSHuBalance/internal/stats"
)

func TestRank(t *testing.T) {
	const a, b, path = "https://a.example", "https://b.example", "/x/feed"
	now := time.Now()
	record := func(s *stats.Stats, u string, o stats.Outcome, n int, d time.Duration) {
		for range n {
			s.Record(u, path, o, now.Add(-d), now)
		}
	}
	cases := []struct {
		name string
		prep func(s *stats.Stats)
		want []string
	}{
		{"failing instance last", func(s *stats.Stats) {
			record(s, a, stats.InstanceFailure, 10, 200*time.Millisecond)
			record(s, b, stats.Success, 10, 200*time.Millisecond)
		}, []string{b, a}},
		{"faster instance first", func(s *stats.Stats) {
			record(s, a, stats.Success, 10, 100*time.Millisecond)
			record(s, b, stats.Success, 10, 2*time.Second)
		}, []string{a, b}},
	}
	for _, c := range cases {
		s := stats.New()
		c.prep(s)
		in := slices.Clone(c.want)
		slices.Reverse(in)
		got := rank(in, path, s, now, rand.New(rand.NewPCG(1, 2)))
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}
