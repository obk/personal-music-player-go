package audio

import (
	"math"
	"sync/atomic"
	"time"
)

var clockBase = time.Now()

// nowNs is a monotonic host time in nanoseconds.
func nowNs() int64 { return int64(time.Since(clockBase)) }

// clock is the playback clock derived from frames actually delivered to the audio device.
//
// The audio callback publishes {frames delivered, host time, generation} through a seqlock (atomics only: no
// locks, no allocation). A reader turns that into a continuously advancing position:
//
//		audible position(now) = frames - latency + (now - hostTime) * rate        (only while advancing)
//
//	  - Between callbacks the position is interpolated, so it moves smoothly at any polling rate (60 Hz frame
//	    animation, lyric selection) instead of stepping once per callback or timer tick.
//	  - Interpolation is clamped to a short horizon: if callbacks stop (underrun, stall) the clock holds.
//	  - A generation counter is bumped by the audio thread on every flush (seek, track change). The position never
//	    drops below the generation's start, and the monotonic hold is reset when the generation changes, so
//	    interpolation never runs across a discontinuity.
//	  - Within a generation the returned position never moves backwards.
//
// Threading: publish from the audio thread only; positionSeconds from a single reader.
type clock struct {
	seq                      atomic.Uint32
	frames, hostNs, genStart atomic.Int64
	gen                      atomic.Uint32
	advancing                atomic.Bool
	rate                     int
	latencyFrames            int64
	horizonSeconds           float64
	lastGen                  uint32 // reader state
	lastPos                  float64
}

func newClock() *clock { return &clock{horizonSeconds: 0.05, lastGen: math.MaxUint32} }

// configure runs before the device starts. latencyFrames: frames queued between the callback and the speaker.
func (c *clock) configure(rate int, latencyFrames, periodFrames int64) {
	c.rate = rate
	c.latencyFrames = latencyFrames
	if rate > 0 {
		c.horizonSeconds = 2*float64(periodFrames)/float64(rate) + 0.02
	} else {
		c.horizonSeconds = 0.05
	}
}

func (c *clock) latencySeconds() float64 {
	if c.rate <= 0 {
		return 0
	}
	return float64(c.latencyFrames) / float64(c.rate)
}

// publish is called on the audio thread only.
func (c *clock) publish(frames, hostNs, genStart int64, gen uint32, advancing bool) {
	s := c.seq.Load()
	c.seq.Store(s + 1) // odd: write in progress
	c.frames.Store(frames)
	c.hostNs.Store(hostNs)
	c.genStart.Store(genStart)
	c.gen.Store(gen)
	c.advancing.Store(advancing)
	c.seq.Store(s + 2) // even: consistent
}

type clockSnapshot struct {
	frames, hostNs, genStart int64
	gen                      uint32
	advancing                bool
}

func (c *clock) read() (clockSnapshot, bool) {
	for attempt := 0; attempt < 1000; attempt++ {
		s1 := c.seq.Load()
		if s1 == 0 {
			return clockSnapshot{}, false // nothing published yet
		}
		if s1&1 != 0 {
			continue
		}
		out := clockSnapshot{c.frames.Load(), c.hostNs.Load(), c.genStart.Load(), c.gen.Load(), c.advancing.Load()}
		if c.seq.Load() == s1 {
			return out, true
		}
	}
	return clockSnapshot{}, false
}

// positionSeconds returns the position at host time now. It holds its last value if nothing has been published.
func (c *clock) positionSeconds(now int64) float64 {
	s, ok := c.read()
	if !ok || c.rate <= 0 {
		return c.lastPos / math.Max(1, float64(c.rate))
	}
	rate := float64(c.rate)
	pos := float64(s.frames) - float64(c.latencyFrames)
	// While the callback keeps delivering, interpolate up to a short horizon (the next callback is due). Once it
	// stops delivering (pause, end), audio already queued in the device still plays out for one latency period,
	// so keep advancing until the position reaches the last delivered frame.
	limit := c.horizonSeconds
	if !s.advancing {
		limit = c.latencySeconds()
	}
	elapsed := math.Max(0, math.Min(float64(now-s.hostNs)*1e-9, limit))
	pos += elapsed * rate
	if !s.advancing {
		pos = math.Min(pos, float64(s.frames))
	}
	pos = math.Max(pos, float64(s.genStart)) // nothing is audible before the generation starts

	if s.gen != c.lastGen {
		c.lastGen = s.gen // discontinuity: start a fresh monotonic run
	} else {
		pos = math.Max(pos, c.lastPos)
	}
	c.lastPos = pos
	return pos / rate
}
