// Package core provides the application's single logical "GUI thread".
//
// All player state (playlist, engine control, lyrics, ...) is owned by one lock. Work that originates elsewhere
// (worker results, timers, media keys, the tray) is posted to the Loop and runs on its goroutine with the lock
// held; the UI takes the same lock for the duration of a frame. So every piece of state is only ever touched by
// one goroutine at a time, as in a Qt application's GUI thread, and needs no locking of its own.
package core

import (
	"sync"
	"time"
)

type Loop struct {
	mu       sync.Mutex
	queue    chan func()
	onChange func()
	done     chan struct{}
}

// NewLoop returns a loop; onChange (may be nil) is called after each batch of posted work, without the lock,
// to let the UI redraw.
func NewLoop(onChange func()) *Loop {
	return &Loop{queue: make(chan func(), 4096), onChange: onChange, done: make(chan struct{})}
}

// SetOnChange replaces the change callback. Call before Run.
func (l *Loop) SetOnChange(f func()) { l.onChange = f }

// Post schedules f to run on the loop with the lock held. Safe from any goroutine, including from inside f.
func (l *Loop) Post(f func()) {
	select {
	case l.queue <- f:
	case <-l.done:
	}
}

// Run executes posted work until Stop. Each batch of queued functions runs under a single lock acquisition.
func (l *Loop) Run() {
	for {
		select {
		case f := <-l.queue:
			l.mu.Lock()
			f()
		drain:
			for {
				select {
				case g := <-l.queue:
					g()
				default:
					break drain
				}
			}
			l.mu.Unlock()
			if l.onChange != nil {
				l.onChange()
			}
		case <-l.done:
			return
		}
	}
}

func (l *Loop) Stop() {
	select {
	case <-l.done:
	default:
		close(l.done)
	}
}

// Lock takes the state lock (the UI does so while it lays out a frame and handles its input).
func (l *Loop) Lock()   { l.mu.Lock() }
func (l *Loop) Unlock() { l.mu.Unlock() }

// Do runs f with the lock held and waits for it (for callers outside the loop, e.g. at shutdown).
func (l *Loop) Do(f func()) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f()
}

// Timer is a restartable single-shot or repeating timer whose callback runs on the loop. Start, Stop and Active
// must be called on the loop (with the lock held).
type Timer struct {
	loop     *Loop
	interval time.Duration
	repeat   bool
	fn       func()
	gen      uint64 // bumped by Start/Stop so a callback already in flight for an old arming is ignored
	t        *time.Timer
	active   bool
}

func (l *Loop) NewTimer(interval time.Duration, repeat bool, fn func()) *Timer {
	return &Timer{loop: l, interval: interval, repeat: repeat, fn: fn}
}

// Start (re)arms the timer.
func (t *Timer) Start() {
	t.Stop()
	t.active = true
	t.gen++
	t.arm(t.gen)
}

func (t *Timer) arm(gen uint64) {
	t.t = time.AfterFunc(t.interval, func() {
		t.loop.Post(func() {
			if gen != t.gen || !t.active {
				return
			}
			if t.repeat {
				t.arm(gen)
			} else {
				t.active = false
			}
			t.fn()
		})
	})
}

func (t *Timer) Stop() {
	if t.t != nil {
		t.t.Stop()
		t.t = nil
	}
	t.active = false
	t.gen++
}

func (t *Timer) Active() bool { return t.active }
