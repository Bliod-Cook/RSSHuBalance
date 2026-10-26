package pool

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func load(t *testing.T, path string) *Pool {
	t.Helper()
	p, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestChangesPersistAcrossReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instances.json")
	p := load(t, path)
	if got := p.List(); len(got) != 0 {
		t.Fatalf("missing state file should give an empty pool, got %v", got)
	}

	got, err := p.Upsert(Instance{URL: "https://b.example/"})
	if err != nil {
		t.Fatal(err)
	}
	if want := (Instance{URL: "https://b.example", MaxConcurrency: 4}); got != want {
		t.Fatalf("upsert returned %+v, want %+v", got, want)
	}
	for _, in := range []Instance{{URL: "https://a.example", MaxConcurrency: 2}, {URL: "https://c.example"}, {URL: "https://b.example", MaxConcurrency: 8}} {
		if _, err := p.Upsert(in); err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := p.Delete("https://c.example/"); !ok || err != nil {
		t.Fatalf("delete existing: %v %v", ok, err)
	}
	if ok, err := p.Delete("https://c.example"); ok || err != nil {
		t.Fatalf("delete missing: %v %v", ok, err)
	}

	want := []Instance{{URL: "https://a.example", MaxConcurrency: 2}, {URL: "https://b.example", MaxConcurrency: 8}}
	if got := load(t, path).List(); !slices.Equal(got, want) {
		t.Fatalf("reloaded %v, want %v", got, want)
	}
}

func TestUpsertRejectsInvalidInstance(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instances.json")
	p := load(t, path)
	for _, in := range []Instance{
		{URL: "ftp://a.example"},
		{URL: "a.example"},
		{URL: "https://"},
		{URL: "https://a.example", MaxConcurrency: -1},
	} {
		if _, err := p.Upsert(in); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: got %v, want ErrInvalid", in, err)
		}
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("state file should not be written, stat: %v", err)
	}
}

func TestLoadRejectsCorruptStateFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "instances.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected error")
	}
}

func TestConcurrencySlotsAndCooldown(t *testing.T) {
	const a = "https://a.example"
	now := time.Now()
	p := load(t, filepath.Join(t.TempDir(), "instances.json"))
	if _, err := p.Upsert(Instance{URL: a, MaxConcurrency: 1}); err != nil {
		t.Fatal(err)
	}

	if got := p.TryAcquire(a, now); got != Acquired {
		t.Fatalf("first acquire: %v", got)
	}
	if got := p.TryAcquire(a, now); got != Full {
		t.Fatalf("second acquire: %v, want Full", got)
	}
	changed := p.Changed()
	p.Release(a)
	select {
	case <-changed:
	default:
		t.Fatal("release should signal Changed")
	}
	if got := p.TryAcquire(a, now); got != Acquired {
		t.Fatalf("acquire after release: %v", got)
	}
	p.Release(a)

	p.Cooldown(a, now.Add(time.Minute))
	if got := p.TryAcquire(a, now); got != Unavailable {
		t.Fatalf("acquire during cooldown: %v, want Unavailable", got)
	}
	if got := p.Available(now); len(got) != 0 {
		t.Fatalf("available during cooldown: %v", got)
	}
	if got := p.Available(now.Add(2 * time.Minute)); !slices.Equal(got, []string{a}) {
		t.Fatalf("available after cooldown: %v", got)
	}
	if got := p.TryAcquire("https://missing.example", now); got != Unavailable {
		t.Fatalf("acquire missing instance: %v, want Unavailable", got)
	}
}
