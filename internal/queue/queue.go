// Package queue is what plays next. It has three parts:
//
//   - the current track;
//   - Up Next: tracks the user queued by hand ("Play Next", "Add to Queue"); they play first and are never
//     replaced by starting another list;
//   - the context: the list playback was started from ("From: Albums › Paranoid"), in play order (shuffled or
//     not), continuing after the current position.
//
// Starting a different list replaces only the context, and can be undone. Must be used on the loop.
package queue

import (
	"math/rand/v2"
	"slices"
)

type RepeatMode int

const (
	RepeatOff RepeatMode = iota
	RepeatAll
	RepeatOne
)

// Context is the list playback came from. Ref identifies it for navigation (e.g. "album:<key>").
type Context struct {
	Name string
	Ref  string
	IDs  []string
}

// Events are called synchronously. Any may be nil.
type Events struct {
	CurrentChanged func(id string) // play this track (may be the same one again)
	CurrentCleared func()          // nothing to play any more
	Changed        func()          // Up Next, the context or the modes changed
}

type state struct {
	current string
	fromCtx bool
	upNext  []string
	ctx     Context
	order   []int
	pos     int
}

type Queue struct {
	state
	shuffle bool
	repeat  RepeatMode
	history []string
	undo    *snapshot
	ev      Events
	rng     *rand.Rand
}

type snapshot struct {
	state
	positionMs int64
}

func New(ev Events) *Queue {
	return &Queue{state: state{pos: -1}, ev: ev, rng: rand.New(rand.NewPCG(rand.Uint64(), rand.Uint64()))}
}

func (q *Queue) Current() string          { return q.current }
func (q *Queue) UpNext() []string         { return q.upNext }
func (q *Queue) Context() Context         { return q.ctx }
func (q *Queue) Shuffle() bool            { return q.shuffle }
func (q *Queue) RepeatMode() RepeatMode   { return q.repeat }
func (q *Queue) CanUndo() bool            { return q.undo != nil }
func (q *Queue) Empty() bool              { return q.current == "" && len(q.upNext) == 0 && len(q.ctx.IDs) == 0 }
func (q *Queue) CurrentFromContext() bool { return q.current != "" && q.fromCtx }

// ContextRest is the rest of the context in play order, after the current position.
func (q *Queue) ContextRest() []string {
	var out []string
	for i := q.pos + 1; i < len(q.order); i++ {
		out = append(out, q.ctx.IDs[q.order[i]])
	}
	return out
}

func (q *Queue) changed() {
	if q.ev.Changed != nil {
		q.ev.Changed()
	}
}

func (q *Queue) play(id string, fromCtx bool, remember bool) {
	if remember && q.current != "" && q.current != id {
		q.history = append(q.history, q.current)
		if len(q.history) > 500 {
			q.history = q.history[len(q.history)-500:]
		}
	}
	q.current, q.fromCtx = id, fromCtx
	if q.ev.CurrentChanged != nil {
		q.ev.CurrentChanged(id)
	}
	q.changed()
}

func (q *Queue) clear() {
	q.current, q.fromCtx = "", false
	if q.ev.CurrentCleared != nil {
		q.ev.CurrentCleared()
	}
	q.changed()
}

// buildOrder sets the play order over the context; with shuffle, first (an index into ctx.IDs, or -1) leads.
func (q *Queue) buildOrder(first int) {
	n := len(q.ctx.IDs)
	q.order = make([]int, n)
	for i := range q.order {
		q.order[i] = i
	}
	if q.shuffle {
		q.rng.Shuffle(n, func(i, j int) { q.order[i], q.order[j] = q.order[j], q.order[i] })
		if first >= 0 {
			k := slices.Index(q.order, first)
			q.order[0], q.order[k] = q.order[k], q.order[0]
		}
	}
	q.pos = -1
	if first >= 0 {
		q.pos = slices.Index(q.order, first)
	}
}

// PlayContext starts playing ctx.IDs[start], with ctx as the new context. Up Next is kept. It reports whether a
// different, non-empty context was replaced (the caller offers Undo then); positionMs is where the replaced
// track was, so Undo can resume it.
func (q *Queue) PlayContext(ctx Context, start int, positionMs int64) (replaced bool) {
	if start < 0 || start >= len(ctx.IDs) {
		return false
	}
	replaced = len(q.ctx.IDs) > 0 && q.ctx.Ref != ctx.Ref
	if replaced {
		prev := q.state
		prev.upNext = slices.Clone(q.upNext)
		q.undo = &snapshot{prev, positionMs}
	} else {
		q.undo = nil
	}
	q.ctx = Context{Name: ctx.Name, Ref: ctx.Ref, IDs: slices.Clone(ctx.IDs)}
	q.buildOrder(start)
	q.play(ctx.IDs[start], true, true)
	return replaced
}

// Undo restores the context (and track) that the last PlayContext replaced. It returns the track to resume and
// where, or ok=false.
func (q *Queue) Undo() (id string, positionMs int64, ok bool) {
	if q.undo == nil {
		return "", 0, false
	}
	s := q.undo
	q.undo = nil
	upNext := q.upNext // hand-queued tracks added since stay
	q.state = s.state
	for _, v := range upNext {
		if !slices.Contains(q.upNext, v) {
			q.upNext = append(q.upNext, v)
		}
	}
	if q.current == "" {
		q.clear()
		return "", 0, true
	}
	q.play(q.current, q.fromCtx, true)
	return q.current, s.positionMs, true
}

// PlayNext puts ids at the front of Up Next.
func (q *Queue) PlayNext(ids []string) {
	q.upNext = slices.Insert(q.upNext, 0, ids...)
	q.changed()
}

// AddToQueue appends ids to Up Next.
func (q *Queue) AddToQueue(ids []string) {
	q.upNext = append(q.upNext, ids...)
	q.changed()
}

// InsertUpNext inserts ids at index at of Up Next.
func (q *Queue) InsertUpNext(at int, ids []string) {
	at = max(0, min(at, len(q.upNext)))
	q.upNext = slices.Insert(q.upNext, at, ids...)
	q.changed()
}

// RemoveUpNext removes the Up Next entries at indexes.
func (q *Queue) RemoveUpNext(indexes []int) {
	drop := map[int]bool{}
	for _, i := range indexes {
		drop[i] = true
	}
	out := q.upNext[:0]
	for i, v := range q.upNext {
		if !drop[i] {
			out = append(out, v)
		}
	}
	q.upNext = out
	q.changed()
}

// MoveUpNext moves the entries at indexes so they start at index to of the list without them.
func (q *Queue) MoveUpNext(indexes []int, to int) {
	pick := map[int]bool{}
	for _, i := range indexes {
		pick[i] = true
	}
	var moved, rest []string
	for i, v := range q.upNext {
		if pick[i] {
			moved = append(moved, v)
		} else {
			rest = append(rest, v)
		}
	}
	to = max(0, min(to, len(rest)))
	q.upNext = slices.Insert(rest, to, moved...)
	q.changed()
}

func (q *Queue) ClearUpNext() {
	q.upNext = nil
	q.changed()
}

// PlayUpNextAt plays Up Next entry i (the entries before it stay queued).
func (q *Queue) PlayUpNextAt(i int) {
	if i < 0 || i >= len(q.upNext) {
		return
	}
	id := q.upNext[i]
	q.upNext = slices.Delete(q.upNext, i, i+1)
	q.play(id, false, true)
}

// PlayContextRestAt plays entry i of ContextRest, skipping the ones before it.
func (q *Queue) PlayContextRestAt(i int) {
	p := q.pos + 1 + i
	if i < 0 || p >= len(q.order) {
		return
	}
	q.pos = p
	q.play(q.ctx.IDs[q.order[p]], true, true)
}

func (q *Queue) nextID() (id string, fromCtx bool, ok bool) {
	if len(q.upNext) > 0 {
		id = q.upNext[0]
		q.upNext = q.upNext[1:]
		return id, false, true
	}
	if q.pos+1 < len(q.order) {
		q.pos++
		return q.ctx.IDs[q.order[q.pos]], true, true
	}
	if q.repeat == RepeatAll && len(q.order) > 0 {
		if q.shuffle {
			cur := -1
			if q.pos >= 0 && q.pos < len(q.order) {
				cur = q.order[q.pos]
			}
			q.buildOrder(-1)
			if len(q.order) > 1 && q.order[0] == cur {
				q.order[0], q.order[1] = q.order[1], q.order[0] // not the same track twice in a row
			}
		}
		q.pos = 0
		return q.ctx.IDs[q.order[0]], true, true
	}
	return "", false, false
}

// Next is the manual "next". It returns false if nothing follows.
func (q *Queue) Next() bool {
	id, fromCtx, ok := q.nextID()
	if !ok {
		return false
	}
	q.play(id, fromCtx, true)
	return true
}

// Previous goes back to the track played before, returning false if there is none.
func (q *Queue) Previous() bool {
	if len(q.history) == 0 {
		return false
	}
	id := q.history[len(q.history)-1]
	q.history = q.history[:len(q.history)-1]
	// Back into the context: continue from that point.
	fromCtx := false
	for p, i := range q.order {
		if q.ctx.IDs[i] == id {
			q.pos, fromCtx = p, true
			break
		}
	}
	if q.current != "" && !q.fromCtx {
		q.upNext = slices.Insert(q.upNext, 0, q.current) // a hand-queued track goes back to the front
	}
	q.play(id, fromCtx, false)
	return true
}

// OnTrackFinished advances automatically, honouring Repeat One.
func (q *Queue) OnTrackFinished() {
	if q.repeat == RepeatOne && q.current != "" {
		q.play(q.current, q.fromCtx, false)
		return
	}
	q.Next()
}

func (q *Queue) SetShuffle(on bool) {
	if on == q.shuffle {
		return
	}
	q.shuffle = on
	cur := -1
	if q.fromCtx && q.pos >= 0 && q.pos < len(q.order) {
		cur = q.order[q.pos]
	}
	q.buildOrder(cur)
	if cur < 0 && q.current != "" {
		q.pos = -1
	}
	q.changed()
}

func (q *Queue) CycleRepeat() {
	q.repeat = (q.repeat + 1) % 3
	q.changed()
}

// Remove drops tracks that left the library. If the current one goes, the next plays (or playback clears).
func (q *Queue) Remove(ids []string) {
	gone := map[string]bool{}
	for _, id := range ids {
		gone[id] = true
	}
	q.upNext = slices.DeleteFunc(q.upNext, func(v string) bool { return gone[v] })
	q.history = slices.DeleteFunc(q.history, func(v string) bool { return gone[v] })
	q.undo = nil
	if len(q.ctx.IDs) > 0 {
		// Keep the play order of the survivors, and the position.
		keep := make([]int, len(q.ctx.IDs)) // old index -> new index, or -1
		var ids []string
		for i, v := range q.ctx.IDs {
			keep[i] = -1
			if !gone[v] {
				keep[i] = len(ids)
				ids = append(ids, v)
			}
		}
		var order []int
		pos := -1
		for p, i := range q.order {
			if keep[i] >= 0 {
				order = append(order, keep[i])
			}
			if p == q.pos {
				pos = len(order) - 1
			}
		}
		q.ctx.IDs, q.order, q.pos = ids, order, pos
	}
	if gone[q.current] {
		if !q.Next() {
			q.clear()
		}
		return
	}
	q.changed()
}
