package eq

import (
	"encoding/binary"
	"math"
	"sync/atomic"
)

// IntFormat is an integer device sample format. S24 is packed 3 bytes; S32 carries 24-bit audio as value << 8.
type IntFormat int

const (
	S16 IntFormat = iota
	S24
	S32
)

type params struct {
	preamp float64
	bands  [Bands]Biquad
	bypass bool
}

func identityParams() params {
	p := params{preamp: 1, bypass: true}
	for i := range p.bands {
		p.bands[i] = Identity
	}
	return p
}

const fresh = 4

// Chain does tone shaping in the audio callback: preamp, then 10 peaking biquads (transposed direct form II,
// per-channel state, double precision) on interleaved stereo.
//
// Threads: Configure and SetSettings on the control goroutine; Process* on the audio thread only. Parameters travel
// through a wait-free triple buffer (the writer never blocks, the reader always gets the latest complete set) and
// every coefficient is ramped linearly over ~10 ms, so moving a slider never steps the output (zipper noise).
//
// True bypass: with all gains at 0 dB (or the EQ off), once a ramp to flat has finished and the filter state has
// emptied, Process* return without touching the samples, so the output is bit-identical to no EQ at all.
type Chain struct {
	// ---- control side ----
	settings Settings
	rate     int

	// ---- triple buffer: slots[writer] is the writer's, slots[reader] the audio thread's, middle the one in
	// between (index in the low bits, fresh set when it holds a set the reader has not seen) ----
	slots  [3]params
	writer int32
	middle atomic.Int32
	reader int32

	// ---- audio thread ----
	current, target, step params // step: per-sample increments while ramping
	rampFrames            uint32
	rampLeft              uint32
	running               bool
	state                 [2][Bands][2]float64 // [channel][band][s1, s2]
	bandActive            [Bands]bool
	scratch               []float32
	rng                   uint32
}

func NewChain() *Chain {
	c := &Chain{writer: 0, reader: 2, rampFrames: 480, rng: 0x12345678}
	c.middle.Store(1)
	for i := range c.slots {
		c.slots[i] = identityParams()
	}
	c.current, c.target = identityParams(), identityParams()
	return c
}

// Configure is called when the device is (re)opened: new rate, all state reset. The callback must not be running.
// maxFrames sizes the scratch buffer used by ProcessInt (larger callbacks are processed in pieces).
func (c *Chain) Configure(sampleRate int, maxFrames uint32, rampMs float64) {
	c.rate = sampleRate
	c.rampFrames = max(1, uint32(float64(sampleRate)*rampMs/1000))
	c.scratch = make([]float32, max(64, maxFrames)*2)
	// Audio-side state starts over (the callback is not running).
	c.current, c.target = identityParams(), identityParams()
	c.rampLeft = 0
	c.running = false
	c.state = [2][Bands][2]float64{}
	c.bandActive = [Bands]bool{}
	c.SetSettings(c.settings) // coefficients depend on the rate
}

func (c *Chain) SetSettings(s Settings) {
	c.settings = s
	p := &c.slots[c.writer]
	*p = identityParams()
	p.bypass = s.IsFlat()
	if !p.bypass {
		p.preamp = math.Pow(10, s.PreampDb/20)
		for b := 0; b < Bands; b++ {
			p.bands[b] = Peaking(float64(c.rate), Frequencies[b], Q, s.GainsDb[b])
		}
	}
	// Publish: hand the filled slot over and take back whichever slot was in the middle.
	c.writer = c.middle.Swap(c.writer|fresh) & 3
}

func (c *Chain) Settings() Settings { return c.settings }

// BypassTarget is the control-side view: will the chain end up bypassed?
func (c *Chain) BypassTarget() bool { return c.settings.IsFlat() }

// Running reports (on the audio thread) whether the chain is processing.
func (c *Chain) Running() bool { return c.running }

func lerpStep(from, to Biquad, n float64) Biquad {
	return Biquad{(to.B0 - from.B0) / n, (to.B1 - from.B1) / n, (to.B2 - from.B2) / n, (to.A1 - from.A1) / n,
		(to.A2 - from.A2) / n}
}

func (c *Chain) pullParams() {
	if c.middle.Load()&fresh == 0 {
		return
	}
	c.reader = c.middle.Swap(c.reader) & 3
	next := &c.slots[c.reader]
	if !c.running {
		if next.bypass {
			c.target = *next
			return // still flat: stay bypassed
		}
		// Start from the exact identity with empty state: the first samples are unchanged, then the ramp begins.
		c.running = true
		c.current = identityParams()
		c.state = [2][Bands][2]float64{}
	}
	c.target = *next
	n := float64(c.rampFrames)
	c.step.preamp = (c.target.preamp - c.current.preamp) / n
	for b := 0; b < Bands; b++ {
		c.step.bands[b] = lerpStep(c.current.bands[b], c.target.bands[b], n)
		c.bandActive[b] = c.bandActive[b] || !c.current.bands[b].IsIdentity() || !c.target.bands[b].IsIdentity()
	}
	c.rampLeft = c.rampFrames
}

func (c *Chain) run(buf []float32, frames int) {
	for i := 0; i < frames; i++ {
		if c.rampLeft > 0 {
			c.rampLeft--
			if c.rampLeft == 0 {
				c.current = c.target // land exactly on the target (no accumulated rounding)
			} else {
				c.current.preamp += c.step.preamp
				for b := 0; b < Bands; b++ {
					cb, d := &c.current.bands[b], &c.step.bands[b]
					cb.B0 += d.B0
					cb.B1 += d.B1
					cb.B2 += d.B2
					cb.A1 += d.A1
					cb.A2 += d.A2
				}
			}
		}
		for ch := 0; ch < 2; ch++ {
			v := float64(buf[2*i+ch]) * c.current.preamp
			for b := 0; b < Bands; b++ {
				if !c.bandActive[b] {
					continue
				}
				cb := &c.current.bands[b]
				s := &c.state[ch][b]
				y := cb.B0*v + s[0] // transposed direct form II
				s[0] = cb.B1*v - cb.A1*y + s[1]
				s[1] = cb.B2*v - cb.A2*y
				v = y
			}
			buf[2*i+ch] = float32(v)
		}
	}
	if c.rampLeft > 0 {
		return
	}
	// A band back at the identity drains its state in two samples (s1 <- s2, s2 <- 0); then it is skipped.
	anyActive := false
	for b := 0; b < Bands; b++ {
		if c.bandActive[b] && c.current.bands[b].IsIdentity() && c.state[0][b] == [2]float64{} &&
			c.state[1][b] == [2]float64{} {
			c.bandActive[b] = false
		}
		anyActive = anyActive || c.bandActive[b]
	}
	if c.target.bypass && !anyActive && c.current.preamp == 1 {
		c.running = false // true bypass from the next buffer on
	}
}

// Process filters interleaved float32 stereo in place. It returns false (and leaves the buffer untouched) while
// bypassed.
func (c *Chain) Process(interleaved []float32, frames int) bool {
	c.pullParams()
	if !c.running {
		return false
	}
	c.run(interleaved, frames)
	return true
}

func (c *Chain) xorshift() uint32 {
	s := c.rng
	s ^= s << 13
	s ^= s >> 17
	s ^= s << 5
	c.rng = s
	return s
}

// ProcessInt filters an integer device buffer: converted to float with FFmpeg's scale, processed, and converted
// back with TPDF dither at 16 or 24 bits. It returns false (and leaves the buffer untouched) while bypassed.
func (c *Chain) ProcessInt(interleaved []byte, format IntFormat, frames int) bool {
	c.pullParams()
	if !c.running {
		return false
	}
	// Same scale as FFmpeg's integer -> float conversion, so a 24-bit sample survives the round trip exactly when
	// nothing else changes it. Dither and rounding happen at 16 bits (S16) or 24 bits (S24, and S32 which carries
	// 24-bit audio in its top 3 bytes).
	scale := 8388608.0
	if format == S16 {
		scale = 32768.0
	}
	inv := 1 / scale
	lo, hi := -scale, scale-1
	chunk := len(c.scratch) / 2
	bps := 4
	switch format {
	case S16:
		bps = 2
	case S24:
		bps = 3
	}

	for done := 0; done < frames; {
		n := min(chunk, frames-done)
		p := interleaved[done*2*bps:]
		f := c.scratch
		for i := 0; i < n*2; i++ {
			var v int32
			switch format {
			case S16:
				v = int32(int16(binary.LittleEndian.Uint16(p[i*2:])))
			case S24:
				q := p[i*3:]
				v = int32(uint32(q[0])<<8|uint32(q[1])<<16|uint32(q[2])<<24) >> 8
			default:
				v = int32(binary.LittleEndian.Uint32(p[i*4:])) >> 8
			}
			f[i] = float32(float64(v) * inv)
		}
		c.run(f, n)
		for i := 0; i < n*2; i++ {
			// TPDF dither: the difference of two uniform [0, 1) values, +-1 LSB triangular.
			tpdf := (float64(c.xorshift()) - float64(c.xorshift())) * (1.0 / 4294967296.0)
			x := math.Max(lo, math.Min(hi, math.RoundToEven(float64(f[i])*scale+tpdf)))
			v := int32(x)
			switch format {
			case S16:
				binary.LittleEndian.PutUint16(p[i*2:], uint16(int16(v)))
			case S24:
				q := p[i*3:]
				q[0], q[1], q[2] = byte(v), byte(v>>8), byte(v>>16)
			default:
				binary.LittleEndian.PutUint32(p[i*4:], uint32(v)<<8)
			}
		}
		done += n
	}
	return true
}
