package logbuf

import (
	"reflect"
	"testing"
)

func TestRingKeepsLastItems(t *testing.T) {
	ring := New[int](3)
	for i := 1; i <= 5; i++ {
		ring.Add(i)
	}

	got := ring.Items()
	want := []int{3, 4, 5}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Items() = %#v, want %#v", got, want)
	}
}

func TestRingStateDoesNotCopyRetainedItems(t *testing.T) {
	ring := New[int](2)
	for value := 1; value <= 3; value++ {
		ring.Add(value)
	}

	cursor, retained := ring.State()
	if cursor != 3 || retained != 2 {
		t.Fatalf("State() = %d, %d, want 3, 2", cursor, retained)
	}
}

func TestRingHandlesZeroLimit(t *testing.T) {
	ring := New[int](0)
	ring.Add(1)

	if got := ring.Items(); got != nil {
		t.Fatalf("Items() = %#v, want nil", got)
	}
}

func TestRingItemsSinceReturnsOnlyNewRetainedItems(t *testing.T) {
	ring := New[int](3)
	ring.Add(1)
	ring.Add(2)

	initial, cursor, retained, reset := ring.ItemsSince(0)
	if want := []int{1, 2}; !reflect.DeepEqual(initial, want) {
		t.Fatalf("initial ItemsSince() = %#v, want %#v", initial, want)
	}
	if cursor != 2 || retained != 2 || reset {
		t.Fatalf("initial metadata = cursor %d, retained %d, reset %t", cursor, retained, reset)
	}

	ring.Add(3)
	ring.Add(4)
	delta, cursor, retained, reset := ring.ItemsSince(cursor)
	if want := []int{3, 4}; !reflect.DeepEqual(delta, want) {
		t.Fatalf("delta ItemsSince() = %#v, want %#v", delta, want)
	}
	if cursor != 4 || retained != 3 || reset {
		t.Fatalf("delta metadata = cursor %d, retained %d, reset %t", cursor, retained, reset)
	}
}

func TestRingItemsSinceResetsStaleCursor(t *testing.T) {
	ring := New[int](2)
	for value := 1; value <= 4; value++ {
		ring.Add(value)
	}

	items, cursor, retained, reset := ring.ItemsSince(0)
	if want := []int{3, 4}; !reflect.DeepEqual(items, want) {
		t.Fatalf("ItemsSince() = %#v, want %#v", items, want)
	}
	if cursor != 4 || retained != 2 || !reset {
		t.Fatalf("metadata = cursor %d, retained %d, reset %t", cursor, retained, reset)
	}
}
