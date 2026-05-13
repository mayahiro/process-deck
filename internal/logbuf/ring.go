package logbuf

type Ring[T any] struct {
	limit int
	items []T
	start int
}

func New[T any](limit int) *Ring[T] {
	return &Ring[T]{limit: limit}
}

func (r *Ring[T]) Add(item T) {
	if r.limit <= 0 {
		return
	}
	if len(r.items) < r.limit {
		r.items = append(r.items, item)
		return
	}
	r.items[r.start] = item
	r.start = (r.start + 1) % r.limit
}

func (r *Ring[T]) Items() []T {
	if len(r.items) == 0 {
		return nil
	}
	out := make([]T, 0, len(r.items))
	for i := range r.items {
		out = append(out, r.items[(r.start+i)%len(r.items)])
	}
	return out
}
