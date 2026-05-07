package manager

import (
	"path/filepath"
	"sync"
	"testing"
)

func TestPortRegistry_AllocateInRange(t *testing.T) {
	dir := t.TempDir()
	r := NewPortRegistry(filepath.Join(dir, "ports.txt"))

	port, err := r.AllocateAuthPort("inst1")
	if err != nil {
		t.Fatalf("allocate: %v", err)
	}
	if port < 10000 || port > 59000 {
		t.Fatalf("port %d outside expected range [10000,59000]", port)
	}
}

func TestPortRegistry_RegisterAllocatesQuad(t *testing.T) {
	dir := t.TempDir()
	r := NewPortRegistry(filepath.Join(dir, "ports.txt"))

	port, err := r.AllocateAuthPort("inst1")
	if err != nil {
		t.Fatal(err)
	}

	used, err := r.UsedPorts()
	if err != nil {
		t.Fatal(err)
	}
	want := []int{port, port + 1, port + 2000, port + 5000}
	for _, w := range want {
		if !used[w] {
			t.Fatalf("expected port %d to be registered after AllocateAuthPort, used=%v", w, used)
		}
	}
}

func TestPortRegistry_AvoidsExistingQuadConflict(t *testing.T) {
	dir := t.TempDir()
	r := NewPortRegistry(filepath.Join(dir, "ports.txt"))

	// Force a quad allocated for inst1.
	first, err := r.AllocateAuthPort("inst1")
	if err != nil {
		t.Fatal(err)
	}

	// Allocate many more — none should ever collide on the quad of inst1.
	for i := 0; i < 50; i++ {
		p, err := r.AllocateAuthPort("inst" + string(rune('a'+i)))
		if err != nil {
			t.Fatalf("iter %d: %v", i, err)
		}
		if p == first || p == first+1 || p == first+2000 || p == first+5000 ||
			p+1 == first || p+2000 == first || p+5000 == first {
			t.Fatalf("collision: new=%d first=%d", p, first)
		}
	}
}

func TestPortRegistry_UnregisterRemovesQuad(t *testing.T) {
	dir := t.TempDir()
	r := NewPortRegistry(filepath.Join(dir, "ports.txt"))

	port, _ := r.AllocateAuthPort("inst1")
	if err := r.Unregister("inst1"); err != nil {
		t.Fatal(err)
	}
	used, _ := r.UsedPorts()
	for _, p := range []int{port, port + 1, port + 2000, port + 5000} {
		if used[p] {
			t.Fatalf("port %d still registered after Unregister", p)
		}
	}
}

func TestPortRegistry_AllocateAPIPortSequential(t *testing.T) {
	dir := t.TempDir()
	r := NewPortRegistry(filepath.Join(dir, "ports.txt"))
	r.APIPortStart = 8100

	p1, err := r.AllocateAPIPort("inst1")
	if err != nil {
		t.Fatal(err)
	}
	if p1 != 8100 {
		t.Fatalf("first API port: got %d want 8100", p1)
	}
	p2, err := r.AllocateAPIPort("inst2")
	if err != nil {
		t.Fatal(err)
	}
	if p2 != 8101 {
		t.Fatalf("second API port: got %d want 8101", p2)
	}
}

func TestPortRegistry_ConcurrentAllocationsAreUnique(t *testing.T) {
	dir := t.TempDir()
	r := NewPortRegistry(filepath.Join(dir, "ports.txt"))

	const N = 20
	var wg sync.WaitGroup
	results := make(chan int, N)
	errs := make(chan error, N)

	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p, err := r.AllocateAuthPort("inst" + string(rune('a'+i)))
			if err != nil {
				errs <- err
				return
			}
			results <- p
		}(i)
	}
	wg.Wait()
	close(results)
	close(errs)

	for e := range errs {
		t.Fatal(e)
	}

	seen := map[int]bool{}
	for p := range results {
		// Each allocation registers a quad — none can collide with any other quad.
		quad := []int{p, p + 1, p + 2000, p + 5000}
		for _, q := range quad {
			if seen[q] {
				t.Fatalf("concurrent allocation produced collision at %d", q)
			}
			seen[q] = true
		}
	}
}
