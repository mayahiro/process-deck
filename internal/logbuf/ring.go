package logbuf

type Ring[T any] struct {
	limit    int
	items    []T
	start    int
	revision uint64
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
	} else {
		r.items[r.start] = item
		r.start = (r.start + 1) % r.limit
	}
	if r.revision == ^uint64(0) {
		r.revision = uint64(len(r.items))
	} else {
		r.revision++
	}
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

// State returns the cursor after the newest retained item and the retained
// item count without copying the ring contents.
func (r *Ring[T]) State() (cursor uint64, retained int) {
	return r.revision, len(r.items)
}

// ItemsSince returns retained items added after cursor and the next cursor
//
// retained is the current number of items in the ring. reset reports that
// cursor predates the retained range or is otherwise invalid, in which case
// items contains the complete retained snapshot.
func (r *Ring[T]) ItemsSince(cursor uint64) (items []T, next uint64, retained int, reset bool) {
	retained = len(r.items)
	next = r.revision
	if retained == 0 {
		return nil, next, 0, cursor != next
	}

	earliest := r.revision - uint64(retained)
	if cursor < earliest || cursor > r.revision {
		return r.Items(), next, retained, true
	}

	offset := int(cursor - earliest)
	items = make([]T, 0, retained-offset)
	for index := offset; index < retained; index++ {
		items = append(items, r.items[(r.start+index)%retained])
	}
	return items, next, retained, false
}
