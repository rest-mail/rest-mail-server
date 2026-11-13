package boundedpool

type Pool[Value any] struct {
	available chan Value
	create    func() Value
}

func New[Value any](capacity int, create func() Value) *Pool[Value] {
	return &Pool[Value]{available: make(chan Value, capacity), create: create}
}

func (pool *Pool[Value]) Get() Value {
	select {
	case value := <-pool.available:
		return value
	default:
		return pool.create()
	}
}

func (pool *Pool[Value]) Put(value Value) {
	select {
	case pool.available <- value:
	default:
	}
}
