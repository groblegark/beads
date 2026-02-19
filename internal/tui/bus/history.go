package bus

import (
	"github.com/steveyegge/beads/internal/rpc"
)

// RingBuffer is a fixed-capacity circular buffer for BusSSEEvents.
// It provides O(1) append and O(1) index access.
type RingBuffer struct {
	buf   []rpc.BusSSEEvent
	cap   int
	head  int // index of oldest element
	count int // number of elements in buffer
	total int64 // total events ever appended (including evicted)
}

// NewRingBuffer creates a ring buffer with the given capacity.
func NewRingBuffer(capacity int) *RingBuffer {
	if capacity < 1 {
		capacity = 1
	}
	return &RingBuffer{
		buf: make([]rpc.BusSSEEvent, capacity),
		cap: capacity,
	}
}

// Push appends an event to the buffer. If full, the oldest event is evicted.
// Returns true if an event was evicted.
func (rb *RingBuffer) Push(evt rpc.BusSSEEvent) bool {
	evicted := false
	if rb.count == rb.cap {
		// Overwrite oldest
		rb.buf[rb.head] = evt
		rb.head = (rb.head + 1) % rb.cap
		evicted = true
	} else {
		idx := (rb.head + rb.count) % rb.cap
		rb.buf[idx] = evt
		rb.count++
	}
	rb.total++
	return evicted
}

// Len returns the number of events in the buffer.
func (rb *RingBuffer) Len() int {
	return rb.count
}

// Cap returns the buffer capacity.
func (rb *RingBuffer) Cap() int {
	return rb.cap
}

// Total returns the total number of events ever pushed (including evicted).
func (rb *RingBuffer) Total() int64 {
	return rb.total
}

// Get returns the event at logical index i (0 = oldest in buffer).
// Panics if i is out of range.
func (rb *RingBuffer) Get(i int) rpc.BusSSEEvent {
	if i < 0 || i >= rb.count {
		panic("RingBuffer.Get: index out of range")
	}
	return rb.buf[(rb.head+i)%rb.cap]
}

// Last returns the most recently pushed event.
// Panics if the buffer is empty.
func (rb *RingBuffer) Last() rpc.BusSSEEvent {
	if rb.count == 0 {
		panic("RingBuffer.Last: empty buffer")
	}
	return rb.Get(rb.count - 1)
}

// Slice returns a copy of events from index start to end (exclusive).
// Indices are logical (0 = oldest). Clamped to valid range.
func (rb *RingBuffer) Slice(start, end int) []rpc.BusSSEEvent {
	if start < 0 {
		start = 0
	}
	if end > rb.count {
		end = rb.count
	}
	if start >= end {
		return nil
	}

	result := make([]rpc.BusSSEEvent, end-start)
	for i := start; i < end; i++ {
		result[i-start] = rb.buf[(rb.head+i)%rb.cap]
	}
	return result
}

// Clear empties the buffer but retains capacity.
func (rb *RingBuffer) Clear() {
	rb.head = 0
	rb.count = 0
	// Zero out to allow GC of payloads
	for i := range rb.buf {
		rb.buf[i] = rpc.BusSSEEvent{}
	}
}
