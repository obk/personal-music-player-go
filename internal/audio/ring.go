package audio

import "sync/atomic"

// ring is a lock-free single-producer / single-consumer ring buffer of interleaved stereo frames. The decoder
// goroutine writes, the device callback reads. Positions are absolute frame counts, so full and empty are never
// ambiguous.
type ring struct {
	buf      []byte
	bpf      int    // bytes per frame
	capacity uint64 // frames
	w, r     atomic.Uint64
}

func newRing(frames, bpf int) *ring {
	return &ring{buf: make([]byte, frames*bpf), bpf: bpf, capacity: uint64(frames)}
}

// writable returns the largest contiguous free region, at most maxFrames long, and its length in frames.
func (q *ring) writable(maxFrames int) ([]byte, int) {
	w, r := q.w.Load(), q.r.Load()
	free := q.capacity - (w - r)
	off := w % q.capacity
	n := min(free, q.capacity-off, uint64(maxFrames))
	return q.buf[off*uint64(q.bpf) : (off+n)*uint64(q.bpf)], int(n)
}

func (q *ring) commitWrite(frames int) { q.w.Add(uint64(frames)) }

// readable returns the largest contiguous filled region, at most maxFrames long, and its length in frames.
func (q *ring) readable(maxFrames int) ([]byte, int) {
	w, r := q.w.Load(), q.r.Load()
	avail := w - r
	off := r % q.capacity
	n := min(avail, q.capacity-off, uint64(maxFrames))
	return q.buf[off*uint64(q.bpf) : (off+n)*uint64(q.bpf)], int(n)
}

func (q *ring) commitRead(frames int) { q.r.Add(uint64(frames)) }

func (q *ring) availableRead() uint64 { return q.w.Load() - q.r.Load() }

// drain discards everything written so far. Consumer only.
func (q *ring) drain() { q.r.Store(q.w.Load()) }
