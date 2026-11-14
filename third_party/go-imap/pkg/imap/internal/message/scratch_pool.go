package message

type scratchPool struct {
	available chan any
	create    func() any
	keep      func(any) bool
}

func newScratchPool(create func() any, keep func(any) bool) *scratchPool {
	return &scratchPool{available: make(chan any, 2), create: create, keep: keep}
}

func (pool *scratchPool) Get() any {
	select {
	case value := <-pool.available:
		return value
	default:
		return pool.create()
	}
}

func (pool *scratchPool) Put(value any) {
	if !pool.keep(value) {
		return
	}
	select {
	case pool.available <- value:
	default:
	}
}

func (pool *scratchPool) Clear() {
	for {
		select {
		case <-pool.available:
		default:
			return
		}
	}
}

func ReleaseReadCaches() {
	fieldScratches.Clear()
	keptPieceLists.Clear()
	nameSets.Clear()
	partScratches.Clear()
	base64Chunks.Clear()
	wordBuffers.Clear()
	entityReaders.Clear()
}
