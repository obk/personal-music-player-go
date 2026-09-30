package ui

import (
	"math"
	"time"

	"gioui.org/layout"
	"gioui.org/op"
)

// Motion: colour / opacity fades are time-based; everything that moves or scales uses a spring.
const (
	fast   = 140 * time.Millisecond
	normal = 260 * time.Millisecond
)

// animating is set during a frame by any animation that has not settled; the frame then asks for another.
type frameClock struct {
	now       time.Time
	animating bool
}

var clk frameClock

func beginFrame(now time.Time) {
	clk.now = now
	clk.animating = false
}

func endFrame(gtx layout.Context) {
	if clk.animating {
		gtx.Execute(op.InvalidateCmd{})
	}
}

// fade approaches its target exponentially with the given time constant (reaches ~95% after 3 tau).
type fade struct {
	v, target float32
	last      time.Time
	init      bool
}

// to moves toward target and returns the current value. dur is the time to (nearly) settle.
func (f *fade) to(target float32, dur time.Duration) float32 {
	if !f.init {
		f.v, f.target, f.last, f.init = target, target, clk.now, true
		return f.v
	}
	dt := clk.now.Sub(f.last).Seconds()
	f.last = clk.now
	f.target = target
	if dt > 0.1 {
		dt = 0.1
	}
	tau := dur.Seconds() / 3
	if tau <= 0 {
		f.v = target
		return f.v
	}
	f.v += (target - f.v) * float32(1-math.Exp(-dt/tau))
	if math.Abs(float64(target-f.v)) < 0.002 {
		f.v = target
	} else {
		clk.animating = true
	}
	return f.v
}

// spring is a damped spring toward a target (bouncy scale changes, knob slides).
type spring struct {
	x, v float32
	last time.Time
	init bool
}

const (
	springOmega = 22.0 // stiffness (rad/s)
	springZeta  = 0.42 // damping ratio: lower = bouncier
)

func (s *spring) to(target float32) float32 {
	if !s.init {
		s.x, s.last, s.init = target, clk.now, true
		return s.x
	}
	dt := clk.now.Sub(s.last).Seconds()
	s.last = clk.now
	if dt > 0.05 {
		dt = 0.05
	}
	// Semi-implicit Euler in small steps.
	for dt > 0 {
		h := math.Min(dt, 0.004)
		a := springOmega*springOmega*float64(target-s.x) - 2*springZeta*springOmega*float64(s.v)
		s.v += float32(a * h)
		s.x += float32(float64(s.v) * h)
		dt -= h
	}
	if math.Abs(float64(target-s.x)) < 0.0005 && math.Abs(float64(s.v)) < 0.005 {
		s.x, s.v = target, 0
	} else {
		clk.animating = true
	}
	return s.x
}

// tween runs a one-shot 0 -> 1 animation with an easing curve.
type tween struct {
	start   time.Time
	dur     time.Duration
	running bool
}

func (t *tween) restart(d time.Duration) {
	t.start, t.dur, t.running = clk.now, d, true
}

// progress returns the linear progress 0..1.
func (t *tween) progress() float32 {
	if !t.running {
		return 1
	}
	p := float32(clk.now.Sub(t.start).Seconds() / t.dur.Seconds())
	if p >= 1 {
		t.running = false
		return 1
	}
	clk.animating = true
	return p
}

func easeOutCubic(t float32) float32 { u := 1 - t; return 1 - u*u*u }

func easeInOutCubic(t float32) float32 {
	if t < 0.5 {
		return 4 * t * t * t
	}
	u := -2*t + 2
	return 1 - u*u*u/2
}
