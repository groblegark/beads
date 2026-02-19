package bus

import (
	"fmt"
	"testing"

	"github.com/steveyegge/beads/internal/rpc"
)

func makeEvent(seq uint64) rpc.BusSSEEvent {
	return rpc.BusSSEEvent{
		Stream: "hooks",
		Type:   "PreToolUse",
		Seq:    seq,
		TS:     fmt.Sprintf("2026-01-01T00:00:%02d.000Z", seq%60),
	}
}

func TestRingBuffer_Basic(t *testing.T) {
	rb := NewRingBuffer(5)

	if rb.Len() != 0 {
		t.Fatalf("expected len 0, got %d", rb.Len())
	}
	if rb.Cap() != 5 {
		t.Fatalf("expected cap 5, got %d", rb.Cap())
	}
	if rb.Total() != 0 {
		t.Fatalf("expected total 0, got %d", rb.Total())
	}

	// Push 3 events
	for i := uint64(1); i <= 3; i++ {
		evicted := rb.Push(makeEvent(i))
		if evicted {
			t.Fatalf("unexpected eviction at push %d", i)
		}
	}

	if rb.Len() != 3 {
		t.Fatalf("expected len 3, got %d", rb.Len())
	}
	if rb.Total() != 3 {
		t.Fatalf("expected total 3, got %d", rb.Total())
	}

	// Check order: oldest first
	if rb.Get(0).Seq != 1 {
		t.Fatalf("expected seq 1 at index 0, got %d", rb.Get(0).Seq)
	}
	if rb.Get(2).Seq != 3 {
		t.Fatalf("expected seq 3 at index 2, got %d", rb.Get(2).Seq)
	}
	if rb.Last().Seq != 3 {
		t.Fatalf("expected last seq 3, got %d", rb.Last().Seq)
	}
}

func TestRingBuffer_Eviction(t *testing.T) {
	rb := NewRingBuffer(3)

	// Fill buffer
	for i := uint64(1); i <= 3; i++ {
		rb.Push(makeEvent(i))
	}

	// Push 4th — should evict oldest (seq=1)
	evicted := rb.Push(makeEvent(4))
	if !evicted {
		t.Fatal("expected eviction on push 4")
	}

	if rb.Len() != 3 {
		t.Fatalf("expected len 3, got %d", rb.Len())
	}
	if rb.Total() != 4 {
		t.Fatalf("expected total 4, got %d", rb.Total())
	}

	// Buffer should now be [2, 3, 4]
	if rb.Get(0).Seq != 2 {
		t.Fatalf("expected oldest seq 2, got %d", rb.Get(0).Seq)
	}
	if rb.Get(1).Seq != 3 {
		t.Fatalf("expected seq 3, got %d", rb.Get(1).Seq)
	}
	if rb.Get(2).Seq != 4 {
		t.Fatalf("expected seq 4, got %d", rb.Get(2).Seq)
	}

	// Push 2 more — should evict 2 and 3
	rb.Push(makeEvent(5))
	rb.Push(makeEvent(6))

	// Buffer should now be [4, 5, 6]
	if rb.Get(0).Seq != 4 {
		t.Fatalf("expected oldest seq 4, got %d", rb.Get(0).Seq)
	}
	if rb.Last().Seq != 6 {
		t.Fatalf("expected last seq 6, got %d", rb.Last().Seq)
	}
	if rb.Total() != 6 {
		t.Fatalf("expected total 6, got %d", rb.Total())
	}
}

func TestRingBuffer_Slice(t *testing.T) {
	rb := NewRingBuffer(5)
	for i := uint64(1); i <= 5; i++ {
		rb.Push(makeEvent(i))
	}

	// Full slice
	all := rb.Slice(0, 5)
	if len(all) != 5 {
		t.Fatalf("expected 5 events, got %d", len(all))
	}
	for i, evt := range all {
		if evt.Seq != uint64(i+1) {
			t.Fatalf("expected seq %d at index %d, got %d", i+1, i, evt.Seq)
		}
	}

	// Partial slice
	mid := rb.Slice(1, 4)
	if len(mid) != 3 {
		t.Fatalf("expected 3 events, got %d", len(mid))
	}
	if mid[0].Seq != 2 || mid[2].Seq != 4 {
		t.Fatalf("unexpected slice contents: %v", mid)
	}

	// After eviction, slice should still work
	rb.Push(makeEvent(6))
	rb.Push(makeEvent(7))
	// Buffer: [3, 4, 5, 6, 7]
	all = rb.Slice(0, rb.Len())
	if all[0].Seq != 3 || all[4].Seq != 7 {
		t.Fatalf("unexpected post-eviction slice: first=%d last=%d", all[0].Seq, all[4].Seq)
	}

	// Out-of-range clamping
	clamped := rb.Slice(-5, 100)
	if len(clamped) != 5 {
		t.Fatalf("expected clamped slice len 5, got %d", len(clamped))
	}

	// Empty range
	empty := rb.Slice(3, 3)
	if len(empty) != 0 {
		t.Fatalf("expected empty slice, got %d", len(empty))
	}
}

func TestRingBuffer_Clear(t *testing.T) {
	rb := NewRingBuffer(3)
	rb.Push(makeEvent(1))
	rb.Push(makeEvent(2))

	rb.Clear()
	if rb.Len() != 0 {
		t.Fatalf("expected len 0 after clear, got %d", rb.Len())
	}
	// Total is preserved
	if rb.Total() != 2 {
		t.Fatalf("expected total 2 after clear, got %d", rb.Total())
	}

	// Can push again after clear
	rb.Push(makeEvent(3))
	if rb.Len() != 1 {
		t.Fatalf("expected len 1, got %d", rb.Len())
	}
	if rb.Get(0).Seq != 3 {
		t.Fatalf("expected seq 3, got %d", rb.Get(0).Seq)
	}
}

func TestRingBuffer_MinCapacity(t *testing.T) {
	rb := NewRingBuffer(0)
	if rb.Cap() != 1 {
		t.Fatalf("expected min cap 1, got %d", rb.Cap())
	}
}

func TestRingBuffer_GetPanics(t *testing.T) {
	rb := NewRingBuffer(3)

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on Get(-1)")
		}
	}()
	rb.Get(-1)
}

func TestRingBuffer_LastPanics(t *testing.T) {
	rb := NewRingBuffer(3)

	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on Last() of empty buffer")
		}
	}()
	rb.Last()
}
