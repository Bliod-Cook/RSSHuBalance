// Package stats estimates each instance's success probability and latency.
//
// Success probability is p_up(instance) × p_route(instance, path). Both are
// Beta posteriors over exponentially time-decayed counts; p_route is shrunk
// along path → namespace → instance → Beta(1,1).
package stats

import (
	"math"
	"math/rand/v2"
	"strings"
	"sync"
	"time"
)

type Outcome int

const (
	Success Outcome = iota
	// RouteFailure means the instance answered but the route failed.
	RouteFailure
	InstanceFailure
	// Timeout means the attempt hit the upstream limit.
	Timeout
	// RateLimited is excluded from the statistics.
	RateLimited
)

const (
	upHalfLife          = 30 * time.Minute
	routeHalfLife       = 24 * time.Hour
	shrinkage           = 3.0
	ewmaAlpha           = 0.1
	minNamespaceSamples = 5
	defaultLatency      = time.Second
	z90                 = 1.2816
)

// counter holds success/failure counts that halve every half-life.
type counter struct {
	s, f float64
	at   time.Time
}

func (c counter) decayed(now time.Time, halfLife time.Duration) (s, f float64) {
	d := math.Exp2(-float64(now.Sub(c.at)) / float64(halfLife))
	return c.s * d, c.f * d
}

func (c *counter) add(ok bool, now time.Time, halfLife time.Duration) {
	c.s, c.f = c.decayed(now, halfLife)
	if ok {
		c.s++
	} else {
		c.f++
	}
	c.at = now
}

// latency keeps an EWMA of ln(seconds) for successes (log-normal) and of
// seconds for failures.
type latency struct {
	succN           int
	logMean, logVar float64
	failN           int
	failMean        float64
}

func (l *latency) addSuccess(d time.Duration) {
	x := math.Log(d.Seconds())
	if l.succN == 0 {
		l.logMean = x
	} else {
		diff := x - l.logMean
		l.logMean += ewmaAlpha * diff
		l.logVar = (1 - ewmaAlpha) * (l.logVar + ewmaAlpha*diff*diff)
	}
	l.succN++
}

func (l *latency) addFailure(d time.Duration) {
	x := d.Seconds()
	if l.failN == 0 {
		l.failMean = x
	} else {
		l.failMean += ewmaAlpha * (x - l.failMean)
	}
	l.failN++
}

func (l *latency) successMean() float64 { return math.Exp(l.logMean + l.logVar/2) }

func (l *latency) successP90() float64 { return math.Exp(l.logMean + z90*math.Sqrt(l.logVar)) }

func successes(l *latency) int { return l.succN }

func failures(l *latency) int { return l.failN }

type instance struct {
	up         counter
	lastUp     time.Time
	route      counter
	lat        latency
	namespaces map[string]*namespace
}

type namespace struct {
	route counter
	lat   latency
	paths map[string]*counter
}

type Stats struct {
	mu        sync.Mutex
	instances map[string]*instance
}

func New() *Stats {
	return &Stats{instances: map[string]*instance{}}
}

// Record updates the statistics with an attempt that ran from start to end.
func (s *Stats) Record(instanceURL, path string, o Outcome, start, end time.Time) {
	if o == RateLimited {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	in := s.instances[instanceURL]
	if in == nil {
		in = &instance{namespaces: map[string]*namespace{}}
		s.instances[instanceURL] = in
	}
	ns := in.namespaces[namespaceOf(path)]
	if ns == nil {
		ns = &namespace{paths: map[string]*counter{}}
		in.namespaces[namespaceOf(path)] = ns
	}
	pc := ns.paths[path]
	if pc == nil {
		pc = &counter{}
		ns.paths[path] = pc
	}

	switch o {
	case Success, RouteFailure:
		in.up.add(true, end, upHalfLife)
		in.lastUp = end
	case InstanceFailure:
		in.up.add(false, end, upHalfLife)
	case Timeout:
		if in.lastUp.Before(start) {
			in.up.add(false, end, upHalfLife)
		}
	}
	if o != InstanceFailure {
		ok := o == Success
		in.route.add(ok, end, routeHalfLife)
		ns.route.add(ok, end, routeHalfLife)
		pc.add(ok, end, routeHalfLife)
	}
	if o == Success {
		in.lat.addSuccess(end.Sub(start))
		ns.lat.addSuccess(end.Sub(start))
	} else {
		in.lat.addFailure(end.Sub(start))
		ns.lat.addFailure(end.Sub(start))
	}
}

// Sample draws p_up and p_route once each and returns their product.
func (s *Stats) Sample(instanceURL, path string, now time.Time, rng *rand.Rand) float64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	upA, upB := s.upBeta(instanceURL, now)
	routeA, routeB := s.routeBeta(instanceURL, path, now)
	return sampleBeta(rng, upA, upB) * sampleBeta(rng, routeA, routeB)
}

// Latency returns the expected success and failure latency, defaulting to 1s
// without samples.
func (s *Stats) Latency(instanceURL, path string) (success, failure time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in, ns := s.find(instanceURL, path)
	success, failure = defaultLatency, defaultLatency
	if l := pick(in, ns, successes); l != nil {
		success = seconds(l.successMean())
	}
	if l := pick(in, ns, failures); l != nil {
		failure = seconds(l.failMean)
	}
	return success, failure
}

// HedgeDelay returns the P90 success latency; false without samples.
func (s *Stats) HedgeDelay(instanceURL, path string) (time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in, ns := s.find(instanceURL, path)
	l := pick(in, ns, successes)
	if l == nil {
		return 0, false
	}
	return seconds(l.successP90()), true
}

type Snapshot struct {
	PUp              float64                      `json:"p_up"`
	PRoute           float64                      `json:"p_route"`
	SuccessLatencyMs float64                      `json:"success_latency_ms,omitempty"`
	FailureLatencyMs float64                      `json:"failure_latency_ms,omitempty"`
	Namespaces       map[string]NamespaceSnapshot `json:"namespaces,omitempty"`
}

type NamespaceSnapshot struct {
	PRoute           float64 `json:"p_route"`
	SuccessLatencyMs float64 `json:"success_latency_ms,omitempty"`
	FailureLatencyMs float64 `json:"failure_latency_ms,omitempty"`
}

// Snapshot reports posterior means and latency estimates of an instance.
func (s *Stats) Snapshot(instanceURL string, now time.Time) Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	snap := Snapshot{PUp: mean(s.upBeta(instanceURL, now)), PRoute: 0.5}
	in := s.instances[instanceURL]
	if in == nil {
		return snap
	}
	snap.PRoute = mean(shrink(now, in.route))
	snap.SuccessLatencyMs, snap.FailureLatencyMs = latencyMs(&in.lat)
	snap.Namespaces = map[string]NamespaceSnapshot{}
	for name, ns := range in.namespaces {
		nsSnap := NamespaceSnapshot{PRoute: mean(shrink(now, in.route, ns.route))}
		nsSnap.SuccessLatencyMs, nsSnap.FailureLatencyMs = latencyMs(&ns.lat)
		snap.Namespaces[name] = nsSnap
	}
	return snap
}

func (s *Stats) find(instanceURL, path string) (*instance, *namespace) {
	in := s.instances[instanceURL]
	if in == nil {
		return nil, nil
	}
	return in, in.namespaces[namespaceOf(path)]
}

func (s *Stats) upBeta(instanceURL string, now time.Time) (a, b float64) {
	var c counter
	if in := s.instances[instanceURL]; in != nil {
		c = in.up
	}
	succ, fail := c.decayed(now, upHalfLife)
	return 1 + succ, 1 + fail
}

func (s *Stats) routeBeta(instanceURL, path string, now time.Time) (a, b float64) {
	var instC, nsC, pathC counter
	in, ns := s.find(instanceURL, path)
	if in != nil {
		instC = in.route
	}
	if ns != nil {
		nsC = ns.route
		if pc := ns.paths[path]; pc != nil {
			pathC = *pc
		}
	}
	return shrink(now, instC, nsC, pathC)
}

// shrink returns the route posterior of the last level: the top level starts
// from Beta(1,1), each lower level from shrinkage pseudo-counts at its
// parent's mean.
func shrink(now time.Time, top counter, lower ...counter) (a, b float64) {
	succ, fail := top.decayed(now, routeHalfLife)
	a, b = 1+succ, 1+fail
	for _, c := range lower {
		m := a / (a + b)
		succ, fail = c.decayed(now, routeHalfLife)
		a, b = succ+shrinkage*m, fail+shrinkage*(1-m)
	}
	return a, b
}

// pick prefers the namespace level once it has enough samples, then the
// instance level if it has any.
func pick(in *instance, ns *namespace, samples func(*latency) int) *latency {
	if ns != nil && samples(&ns.lat) >= minNamespaceSamples {
		return &ns.lat
	}
	if in != nil && samples(&in.lat) > 0 {
		return &in.lat
	}
	return nil
}

func latencyMs(l *latency) (success, failure float64) {
	if l.succN > 0 {
		success = l.successMean() * 1000
	}
	if l.failN > 0 {
		failure = l.failMean * 1000
	}
	return success, failure
}

func namespaceOf(path string) string {
	ns, _, _ := strings.Cut(strings.TrimPrefix(path, "/"), "/")
	return ns
}

func mean(a, b float64) float64 { return a / (a + b) }

func seconds(s float64) time.Duration { return time.Duration(s * float64(time.Second)) }
