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

func TestRingHandlesZeroLimit(t *testing.T) {
	ring := New[int](0)
	ring.Add(1)

	if got := ring.Items(); got != nil {
		t.Fatalf("Items() = %#v, want nil", got)
	}
}
