package stats

import (
	"math"
	"math/rand/v2"
	"testing"
	"time"
)

const inst = "https://rsshub.example"

var t0 = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestBetaSampleMean(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 2))
	for _, p := range [][2]float64{{2, 5}, {0.3, 0.7}, {30, 10}} {
		const n = 20000
		sum := 0.0
		for range n {
			sum += sampleBeta(rng, p[0], p[1])
		}
		if got, want := sum/n, mean(p[0], p[1]); !near(got, want, 0.01) {
			t.Errorf("Beta(%v, %v) sample mean = %v, want %v", p[0], p[1], got, want)
		}
	}
}

func TestUpCountsHalveAfterHalfLife(t *testing.T) {
	s := New()
	s.Record(inst, "/x/a", InstanceFailure, t0.Add(-time.Second), t0)
	if a, b := s.upBeta(inst, t0); a != 1 || b != 2 {
		t.Fatalf("at t0: Beta(%v, %v), want Beta(1, 2)", a, b)
	}
	if a, b := s.upBeta(inst, t0.Add(30*time.Minute)); a != 1 || !near(b, 1.5, 1e-9) {
		t.Fatalf("after half-life: Beta(%v, %v), want Beta(1, 1.5)", a, b)
	}
}

func TestRouteShrinksTowardParentLevel(t *testing.T) {
	s := New()
	if a, b := s.routeBeta(inst, "/x/a", t0); a != 1.5 || b != 1.5 {
		t.Fatalf("no data: Beta(%v, %v), want Beta(1.5, 1.5)", a, b)
	}
	for range 4 {
		s.Record(inst, "/x/a", RouteFailure, t0.Add(-time.Second), t0)
	}
	instMean := 1.0 / 6                          // Beta(1, 1+4)
	nsMean := mean(3*instMean, 4+3*(1-instMean)) // = 1/14
	cases := map[string]float64{
		"/x/a": mean(3*nsMean, 4+3*(1-nsMean)),
		"/x/b": nsMean,
		"/y/a": instMean,
	}
	for path, want := range cases {
		if got := mean(s.routeBeta(inst, path, t0)); !near(got, want, 1e-9) {
			t.Errorf("%s: mean %v, want %v", path, got, want)
		}
	}
}

func TestRecordOutcomes(t *testing.T) {
	start, end := t0.Add(-time.Second), t0
	cases := []struct {
		name          string
		record        func(s *Stats)
		pUp, pRoute   float64
		failureLatSet bool
	}{
		{"success", func(s *Stats) { s.Record(inst, "/x/a", Success, start, end) }, 2. / 3, 2. / 3, false},
		{"route failure", func(s *Stats) { s.Record(inst, "/x/a", RouteFailure, start, end) }, 2. / 3, 1. / 3, true},
		{"instance failure", func(s *Stats) { s.Record(inst, "/x/a", InstanceFailure, start, end) }, 1. / 3, 1. / 2, true},
		{"rate limited", func(s *Stats) { s.Record(inst, "/x/a", RateLimited, start, end) }, 1. / 2, 1. / 2, false},
		{"timeout without up success", func(s *Stats) { s.Record(inst, "/x/a", Timeout, start, end) }, 1. / 3, 1. / 3, true},
		{"timeout after up success during attempt", func(s *Stats) {
			s.Record(inst, "/y/a", RouteFailure, start, end)
			s.Record(inst, "/x/a", Timeout, start, end)
		}, 2. / 3, 1. / 4, true},
		{"timeout after up success before attempt", func(s *Stats) {
			s.Record(inst, "/y/a", Success, start.Add(-2*time.Millisecond), start.Add(-time.Millisecond))
			s.Record(inst, "/x/a", Timeout, start, end)
		}, 1. / 2, 1. / 2, true},
	}
	for _, c := range cases {
		s := New()
		c.record(s)
		snap := s.Snapshot(inst, end)
		if !near(snap.PUp, c.pUp, 1e-3) || !near(snap.PRoute, c.pRoute, 1e-3) {
			t.Errorf("%s: p_up=%v p_route=%v, want %v %v", c.name, snap.PUp, snap.PRoute, c.pUp, c.pRoute)
		}
		if (snap.FailureLatencyMs > 0) != c.failureLatSet {
			t.Errorf("%s: failure latency %vms, want set=%v", c.name, snap.FailureLatencyMs, c.failureLatSet)
		}
	}
}

func TestLatencyFallsBackToInstanceLevel(t *testing.T) {
	s := New()
	if succ, fail := s.Latency(inst, "/x/a"); succ != time.Second || fail != time.Second {
		t.Fatalf("no samples: %v %v, want 1s defaults", succ, fail)
	}
	if _, ok := s.HedgeDelay(inst, "/x/a"); ok {
		t.Fatal("no samples: hedge delay should be unavailable")
	}
	for range 5 {
		s.Record(inst, "/fast/a", Success, t0, t0.Add(100*time.Millisecond))
	}
	s.Record(inst, "/slow/a", Success, t0, t0.Add(time.Second))

	if succ, _ := s.Latency(inst, "/fast/b"); !near(succ.Seconds(), 0.1, 1e-6) {
		t.Errorf("namespace with enough samples: %v, want 100ms", succ)
	}
	if succ, _ := s.Latency(inst, "/slow/a"); succ <= 100*time.Millisecond || succ >= time.Second {
		t.Errorf("namespace with too few samples should use instance level, got %v", succ)
	}
}

func TestLatencyEstimates(t *testing.T) {
	s := New()
	s.Record(inst, "/x/a", Success, t0, t0.Add(time.Second))
	s.Record(inst, "/x/a", Success, t0, t0.Add(seconds(math.E)))
	s.Record(inst, "/x/a", InstanceFailure, t0, t0.Add(2*time.Second))
	s.Record(inst, "/x/a", Timeout, t0, t0.Add(4*time.Second))

	// ln samples 0 and 1 with α=0.1: μ=0.1, σ²=0.09
	succ, fail := s.Latency(inst, "/x/a")
	if !near(succ.Seconds(), math.Exp(0.1+0.09/2), 1e-6) {
		t.Errorf("success latency %v", succ)
	}
	if !near(fail.Seconds(), 2.2, 1e-6) {
		t.Errorf("failure latency %v, want 2.2s", fail)
	}
	if p90, ok := s.HedgeDelay(inst, "/x/a"); !ok || !near(p90.Seconds(), math.Exp(0.1+1.2816*0.3), 1e-6) {
		t.Errorf("P90 %v ok=%v", p90, ok)
	}
}

func TestSnapshotNamespaces(t *testing.T) {
	s := New()
	s.Record(inst, "/x/a", Success, t0, t0.Add(200*time.Millisecond))
	snap := s.Snapshot(inst, t0)
	ns, ok := snap.Namespaces["x"]
	if !ok || !near(ns.SuccessLatencyMs, 200, 1e-6) || ns.PRoute <= 0.5 {
		t.Fatalf("namespace snapshot %+v ok=%v", ns, ok)
	}
}
