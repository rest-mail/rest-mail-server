package message

import (
	"runtime"
	"testing"
)

func TestScratchPoolBoundsRetentionAndSurvivesCollections(t *testing.T) {
	pool := newScratchPool(func() any { return new([]byte) }, func(value any) bool { return cap(*value.(*[]byte)) <= 1024 })
	first, second, third := pool.Get(), pool.Get(), pool.Get()
	pool.Put(first)
	pool.Put(second)
	pool.Put(third)
	runtime.GC()
	runtime.GC()
	if pool.Get() != first || pool.Get() != second || pool.Get() == third {
		t.Fatal("scratch retention was not bounded at two across collections")
	}
	large := make([]byte, 1025)
	pool.Put(&large)
	if pool.Get() == &large {
		t.Fatal("scratch retained an oversized buffer")
	}
}

func TestScratchPoolClearsOnlyReturnedValues(t *testing.T) {
	pool := newScratchPool(func() any { return new(int) }, func(any) bool { return true })
	borrowed, returned := pool.Get().(*int), pool.Get().(*int)
	*borrowed = 42
	pool.Put(returned)
	pool.Clear()
	if pool.Get() == returned || *borrowed != 42 {
		t.Fatal("clearing scratch touched a borrowed value or retained a returned one")
	}
}

func TestFixedReadScratchSurvivesCollections(t *testing.T) {
	defer ReleaseReadCaches()
	for _, pool := range []*scratchPool{base64Chunks, wordBuffers, entityReaders} {
		pool.Clear()
		value := pool.Get()
		pool.Put(value)
		runtime.GC()
		runtime.GC()
		if pool.Get() != value {
			t.Fatal("fixed scratch was discarded between parts")
		}
	}
}
