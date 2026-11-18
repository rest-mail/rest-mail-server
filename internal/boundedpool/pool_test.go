package boundedpool

import (
	"runtime"
	"sync"
	"testing"
)

func TestPoolRetainsOnlyItsCapacityAcrossCollections(t *testing.T) {
	created := 0
	pool := New(2, func() *int {
		created++
		return new(int)
	})
	first, second, third := pool.Get(), pool.Get(), pool.Get()
	if created != 3 || first == second || first == third || second == third {
		t.Fatal("borrowers shared a value")
	}
	pool.Put(first)
	pool.Put(second)
	pool.Put(third)
	runtime.GC()
	runtime.GC()
	if pool.Get() != first || pool.Get() != second {
		t.Fatal("collection discarded retained values")
	}
	if pool.Get() == third || created != 4 {
		t.Fatal("pool retained more than its capacity")
	}
}

func TestPoolCanBeUsedConcurrently(t *testing.T) {
	pool := New(4, func() *int { return new(int) })
	var workers sync.WaitGroup
	for range 16 {
		workers.Go(func() {
			for range 100 {
				value := pool.Get()
				*value = *value + 1
				pool.Put(value)
			}
		})
	}
	workers.Wait()
}
