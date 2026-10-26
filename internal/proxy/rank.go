package proxy

import (
	"cmp"
	"math/rand/v2"
	"slices"
	"time"

	"github.com/Bliod-Cook/RSSHuBalance/internal/stats"
)

// rank orders instances by p̃/c, where p̃ is a Thompson sample of the success
// probability and c = p̃·success latency + (1−p̃)·failure latency.
func rank(urls []string, path string, st *stats.Stats, now time.Time, rng *rand.Rand) []string {
	score := make(map[string]float64, len(urls))
	for _, u := range urls {
		p := st.Sample(u, path, now, rng)
		succ, fail := st.Latency(u, path)
		score[u] = p / (p*succ.Seconds() + (1-p)*fail.Seconds())
	}
	ranked := slices.Clone(urls)
	slices.SortFunc(ranked, func(a, b string) int { return cmp.Compare(score[b], score[a]) })
	return ranked
}
