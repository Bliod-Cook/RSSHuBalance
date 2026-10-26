// Package pool holds the RSSHub instances, persisted to a JSON state file,
// together with their in-flight counts and cooldowns.
package pool

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

const defaultMaxConcurrency = 4

var ErrInvalid = errors.New("invalid instance")

type Instance struct {
	URL            string `json:"url"`
	MaxConcurrency int    `json:"max_concurrency"`
}

type Acquire int

const (
	Acquired Acquire = iota
	Full
	// Unavailable means the instance is cooling down or no longer exists.
	Unavailable
)

type entry struct {
	Instance
	inflight  int
	coolUntil time.Time
}

type Pool struct {
	mu      sync.Mutex
	path    string
	entries map[string]*entry
	changed chan struct{}
}

// Load reads the state file; a missing file gives an empty pool.
func Load(path string) (*Pool, error) {
	p := &Pool{path: path, entries: map[string]*entry{}, changed: make(chan struct{})}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return nil, err
	}
	var list []Instance
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for _, in := range list {
		p.entries[in.URL] = &entry{Instance: in}
	}
	return p, nil
}

// List returns the instances sorted by URL.
func (p *Pool) List() []Instance {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.instances("")
}

// Upsert adds or updates the instance with the same URL and saves the pool.
func (p *Pool) Upsert(in Instance) (Instance, error) {
	in, err := normalize(in)
	if err != nil {
		return Instance{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := save(p.path, append(p.instances(in.URL), in)); err != nil {
		return Instance{}, err
	}
	if e := p.entries[in.URL]; e != nil {
		e.Instance = in
	} else {
		p.entries[in.URL] = &entry{Instance: in}
	}
	p.notify()
	return in, nil
}

// Delete removes the instance and saves the pool; false if it did not exist.
func (p *Pool) Delete(rawURL string) (bool, error) {
	u := strings.TrimRight(rawURL, "/")
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.entries[u] == nil {
		return false, nil
	}
	if err := save(p.path, p.instances(u)); err != nil {
		return false, err
	}
	delete(p.entries, u)
	p.notify()
	return true, nil
}

// Available returns the URLs of instances not cooling down.
func (p *Pool) Available(now time.Time) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var urls []string
	for u, e := range p.entries {
		if !now.Before(e.coolUntil) {
			urls = append(urls, u)
		}
	}
	return urls
}

// TryAcquire takes a concurrency slot of the instance.
func (p *Pool) TryAcquire(u string, now time.Time) Acquire {
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.entries[u]
	switch {
	case e == nil || now.Before(e.coolUntil):
		return Unavailable
	case e.inflight >= e.MaxConcurrency:
		return Full
	}
	e.inflight++
	return Acquired
}

func (p *Pool) Release(u string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e := p.entries[u]; e != nil && e.inflight > 0 {
		e.inflight--
	}
	p.notify()
}

func (p *Pool) Cooldown(u string, until time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if e := p.entries[u]; e != nil {
		e.coolUntil = until
	}
}

// Changed returns a channel closed on the next release or pool change.
func (p *Pool) Changed() <-chan struct{} {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.changed
}

func (p *Pool) notify() {
	close(p.changed)
	p.changed = make(chan struct{})
}

// instances returns the instances except the one with the given URL.
func (p *Pool) instances(except string) []Instance {
	list := make([]Instance, 0, len(p.entries))
	for u, e := range p.entries {
		if u != except {
			list = append(list, e.Instance)
		}
	}
	sortByURL(list)
	return list
}

func normalize(in Instance) (Instance, error) {
	in.URL = strings.TrimRight(in.URL, "/")
	u, err := url.Parse(in.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return in, fmt.Errorf("%w: url must be an absolute http(s) URL", ErrInvalid)
	}
	if in.MaxConcurrency < 0 {
		return in, fmt.Errorf("%w: max_concurrency must not be negative", ErrInvalid)
	}
	if in.MaxConcurrency == 0 {
		in.MaxConcurrency = defaultMaxConcurrency
	}
	return in, nil
}

// save atomically replaces the state file.
func save(path string, list []Instance) error {
	sortByURL(list)
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func sortByURL(list []Instance) {
	slices.SortFunc(list, func(a, b Instance) int { return strings.Compare(a.URL, b.URL) })
}
