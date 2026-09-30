package audio

/*
#cgo CFLAGS: -I${SRCDIR}/../../lib/miniaudio -O2
#include <stdlib.h>
#include "ma_glue.h"
*/
import "C"

import (
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
	"runtime/cgo"
	"slices"
	"strconv"
	"sync/atomic"
	"time"
	"unicode/utf16"
	"unsafe"

	"musicplayer/internal/core"
	"musicplayer/internal/eq"
)

const (
	chunkFrames        = 4096
	ringMillis         = 750
	tickInterval       = 30 * time.Millisecond
	devicePollInterval = 2 * time.Second
)

type State int

const (
	Stopped State = iota
	Playing
	Paused
)

// OutputDevice is one playback endpoint. ID is stable across restarts and re-plugging (backend + endpoint id),
// never an index.
type OutputDevice struct {
	ID        string
	Name      string
	IsDefault bool
}

// OutputFormat is what the device was actually opened with (driver-reported), plus what the source is.
type OutputFormat struct {
	DeviceName string
	SampleRate int
	Bits       int
	IsFloat    bool
	Exclusive  bool
	BitPerfect bool // samples reach the driver unmodified: no resampling, conversion, gain or dither
	SourceRate int
	SourceBits int
}

// Events are the backend's notifications. They are called on the loop, with its lock held. Any may be nil.
type Events struct {
	PositionChanged     func(frames int64)
	DurationChanged     func(frames int64)
	StateChanged        func(State)
	EndOfStream         func()
	Error               func(msg string)
	Notice              func(msg string) // informational (device switched, exclusive fell back)
	DevicesChanged      func()           // OutputDevices changed (plug / unplug / default change)
	OutputFormatChanged func()           // OutputFormat or VolumeLocked changed
}

type Options struct {
	NullDevice bool // miniaudio's null backend: real-time pacing, no sound (tests, CI)
	// Test hooks.
	RefuseExclusive   bool // behave as if the device refused exclusive mode
	ExclusiveOnlyBits int  // accept exclusive only with this container size (16, 24 = packed, 32)
	// Called on the audio thread with every buffer handed to the device (after gain). Must be real-time safe.
	Tap func(frames []byte, frameCount, bytesPerFrame int)
}

// deviceSpec is how the device is (to be) opened. Two equal specs mean the open device can be reused as is.
type deviceSpec struct {
	id        string // empty = follow the system default device
	exclusive bool
	format    SampleFormat
	rate      int // 0 = the device's own rate (shared mode)
}

type choice struct {
	format SampleFormat
	rate   int
}

// Backend owns the sample path:
//
//	Decoder (decoder goroutine) -> interleaved stereo ring buffer (lock-free SPSC) -> miniaudio data callback
//
// The device callback never blocks, allocates or locks: it only reads the ring buffer, applies gain and updates
// atomics. Everything else (seek, load, end-of-stream, position reporting) is coordinated through atomics and a
// small flush handshake between the decoder goroutine and the callback.
//
// Shared mode: float32 at the device's mix rate, software volume. Exclusive mode: the device is opened at each
// file's own rate and sample format (16-bit, 24-in-32-bit or float), the callback copies samples untouched and
// software volume is off, so integer PCM reaches the driver bit for bit.
//
// Control methods must be called on the loop (with its lock held).
type Backend struct {
	opts   Options
	ev     Events
	handle cgo.Handle

	// ---- device / ring buffer ----
	ctx     *C.ma_context
	dev     *C.ma_device
	rb      *ring
	rate    int
	bpf     int  // bytes per stereo frame on the device (fixed while the device runs)
	rawCopy bool // exclusive: samples go out untouched (no gain, no fades)

	// ---- shared between the control side, the decoder goroutine and the audio callback ----
	flushGen    atomic.Uint32 // control: "discard the ring buffer and continue from seekTarget"
	writerAck   atomic.Uint32 // decoder: "I have stopped writing for this generation"
	consumerAck atomic.Uint32 // callback: "ring buffer drained for this generation"
	seekTarget  atomic.Int64
	pos         atomic.Int64 // frames delivered to the device (output timeline)
	playing     atomic.Bool
	eof         atomic.Bool // decoder has written everything
	ended       atomic.Bool // callback drained everything after eof
	quit        atomic.Bool
	gain        atomic.Uint32 // float32 bits
	underruns   atomic.Uint64
	// Device notifications (arrive on miniaudio's thread; handled on the loop).
	expectStop    atomic.Bool // we are closing the device ourselves
	deviceStopped atomic.Bool // the device stopped without us asking: unplugged or invalidated
	rerouted      atomic.Bool // the default device changed and miniaudio followed it

	// ---- audio-callback private ----
	cbGen         uint32
	curGain       float32
	rampStep      float32
	awaitData     bool  // after a flush: the decoder is repositioning, an empty ring is expected
	posLocal      int64 // callback's own copy of pos
	genStart      int64 // position at which the current flush generation began
	lastDeliverNs int64 // host time of the last callback that delivered audio

	clock     *clock    // published by the callback, read on the loop
	dsp       *eq.Chain // equalizer: parameters from the loop, processing in the callback
	devFormat SampleFormat

	// ---- control side (loop) ----
	dec             *Decoder
	source          SourceInfo
	path            string
	decDone         chan struct{}
	state           State
	durationFrames  int64
	atEnd           bool
	deviceOpen      bool
	openSpec        deviceSpec
	preferredID     string // the user's choice; empty = system default
	exclusivePref   bool
	onFallback      bool // the preferred device is missing; playing on the default meanwhile
	devices         []OutputDevice
	format          OutputFormat
	exclusiveWarned map[string]bool   // device/format combinations already reported as refusing exclusive mode
	exclusiveBest   map[string]choice // device + source format -> format and rate that worked
	tick            *core.Timer
	devicePoll      *core.Timer
}

func NewBackend(loop *core.Loop, opts Options, ev Events) *Backend {
	b := &Backend{
		opts: opts, ev: ev, clock: newClock(), dsp: eq.NewChain(), bpf: 8, awaitData: true,
		exclusiveWarned: map[string]bool{}, exclusiveBest: map[string]choice{},
	}
	b.gain.Store(math.Float32bits(0.8))
	b.handle = cgo.NewHandle(b)
	b.tick = loop.NewTimer(tickInterval, true, b.onTick)
	// miniaudio has no device-list notification, so plug / unplug / default changes are picked up by polling.
	b.devicePoll = loop.NewTimer(devicePollInterval, true, b.RefreshDevices)
	if b.ensureContext() {
		b.devices = b.enumerate()
	}
	b.devicePoll.Start()
	return b
}

// Close stops playback and releases the device and the context.
func (b *Backend) Close() {
	b.tick.Stop()
	b.devicePoll.Stop()
	b.stopDecoder()
	b.closeDevice()
	if b.ctx != nil {
		C.mp_context_free(b.ctx)
		b.ctx = nil
	}
	b.handle.Delete()
}

// ---------------------------------------------------------------------------------------------
// Audio thread. Rules: no allocation, no locks, no logging.
// ---------------------------------------------------------------------------------------------

//export goMaData
func goMaData(h C.uintptr_t, out unsafe.Pointer, frames C.ma_uint32) {
	b := cgo.Handle(h).Value().(*Backend)
	n := int(frames)
	b.dataCallback(unsafe.Slice((*byte)(out), n*b.bpf), n)
}

//export goMaNotify
func goMaNotify(h C.uintptr_t, typ C.int) {
	b := cgo.Handle(h).Value().(*Backend)
	if typ == C.ma_device_notification_type_stopped && !b.expectStop.Load() {
		b.deviceStopped.Store(true)
	} else if typ == C.ma_device_notification_type_rerouted {
		b.rerouted.Store(true)
	}
}

func floats(p []byte, frames int) []float32 {
	if frames == 0 {
		return nil
	}
	return unsafe.Slice((*float32)(unsafe.Pointer(&p[0])), frames*2)
}

func (b *Backend) dataCallback(dst []byte, frames int) {
	bpf := b.bpf

	// Flush handshake: only drain once the decoder has promised not to write any more stale data.
	fg := b.flushGen.Load()
	if fg != b.cbGen {
		if b.writerAck.Load() == fg {
			b.rb.drain()
			target := b.seekTarget.Load()
			b.posLocal, b.genStart = target, target
			b.pos.Store(target)
			b.cbGen = fg
			b.awaitData = true
			// A new generation begins here; publish before acknowledging so readers never pair the new
			// generation with an old sample.
			b.clock.publish(target, nowNs(), target, fg, false)
			b.consumerAck.Store(fg)
		}
		clear(dst)
		if b.opts.Tap != nil {
			b.opts.Tap(dst, frames, bpf)
		}
		return
	}

	wantPlay := b.playing.Load()
	produced := 0

	if b.rawCopy {
		// Exclusive mode: samples go to the driver exactly as decoded. Pausing stops delivery immediately; a fade
		// would alter samples.
		if wantPlay {
			for produced < frames {
				src, n := b.rb.readable(frames - produced)
				if n == 0 {
					break
				}
				copy(dst[produced*bpf:], src)
				b.rb.commitRead(n)
				produced += n
			}
		}
		// The equalizer, if on (and not flat), is the only thing allowed to alter the samples here; it dithers
		// back to the integer format. Flat or off: untouched, still bit-perfect.
		if produced > 0 {
			switch b.devFormat {
			case S16:
				b.dsp.ProcessInt(dst[:produced*bpf], eq.S16, produced)
			case S24:
				b.dsp.ProcessInt(dst[:produced*bpf], eq.S24, produced)
			case S32:
				b.dsp.ProcessInt(dst[:produced*bpf], eq.S32, produced)
			default:
				b.dsp.Process(floats(dst, produced), produced)
			}
		}
	} else {
		var target float32
		if wantPlay {
			target = math.Float32frombits(b.gain.Load())
		}
		step := b.rampStep
		g := b.curGain
		fdst := floats(dst, frames)
		// While paused the ring buffer is left untouched (after the short fade-out completes).
		if wantPlay || g > 0 {
			for produced < frames {
				src, n := b.rb.readable(frames - produced)
				if n == 0 {
					break
				}
				fsrc := floats(src, n)
				d := fdst[produced*2:]
				for i := 0; i < n; i++ {
					if g < target {
						g = min(g+step, target)
					} else if g > target {
						g = max(g-step, target)
					}
					d[2*i] = fsrc[2*i] * g
					d[2*i+1] = fsrc[2*i+1] * g
				}
				b.rb.commitRead(n)
				produced += n
				if !wantPlay && g == 0 {
					break
				}
			}
			b.curGain = g
		}
		if produced > 0 {
			b.dsp.Process(fdst, produced)
		}
	}
	if produced > 0 {
		b.posLocal += int64(produced)
		b.pos.Store(b.posLocal)
		b.awaitData = false
	}
	// Frames delivered so far and when: the reader interpolates from this. Time only advances while this callback
	// actually delivered audio (paused, waiting for data and starved callbacks freeze it). Idle callbacks keep the
	// last delivery time, so the reader's coast through the queued audio is measured from when delivery stopped
	// rather than restarting every callback (which crept forward with callback jitter).
	now := nowNs()
	hostNs := b.lastDeliverNs
	if produced > 0 {
		b.lastDeliverNs = now
		hostNs = now
	}
	b.clock.publish(b.posLocal, hostNs, b.genStart, b.cbGen, produced > 0)

	if produced < frames {
		clear(dst[produced*bpf:])
	}
	if b.opts.Tap != nil {
		b.opts.Tap(dst, frames, bpf)
	}

	if wantPlay && produced < frames {
		if b.eof.Load() {
			if b.rb.availableRead() == 0 { // everything the decoder wrote has been played
				b.ended.Store(true)
			}
		} else if !b.awaitData {
			b.underruns.Add(1) // genuine starvation mid-stream
		}
	}
}

// ---------------------------------------------------------------------------------------------
// Decoder goroutine
// ---------------------------------------------------------------------------------------------

func (b *Backend) decodeLoop(dec *Decoder, rb *ring, done chan struct{}) {
	defer close(done)
	var gen uint32
	atEOF := false

	for !b.quit.Load() {
		g := b.flushGen.Load()
		if g != gen {
			// 1. stop writing and tell the callback; 2. wait for it to drain; 3. reposition; 4. resume.
			gen = g
			atEOF = false
			b.eof.Store(false)
			b.writerAck.Store(g)
			for !b.quit.Load() && b.consumerAck.Load() != g && b.flushGen.Load() == g {
				time.Sleep(time.Millisecond)
			}
			if b.quit.Load() {
				break
			}
			if b.flushGen.Load() != g {
				continue // superseded by a newer request
			}
			if !dec.SeekTo(b.seekTarget.Load()) {
				atEOF = true
				b.eof.Store(true)
			}
			continue
		}

		if atEOF {
			time.Sleep(5 * time.Millisecond)
			continue
		}

		region, n := rb.writable(chunkFrames)
		if n == 0 {
			time.Sleep(2 * time.Millisecond) // ring is full
			continue
		}
		got := dec.Read(region)
		rb.commitWrite(got)
		if got == 0 {
			atEOF = true
			b.eof.Store(true)
		}
	}
}

// ---------------------------------------------------------------------------------------------
// Control side (loop)
// ---------------------------------------------------------------------------------------------

func toMa(f SampleFormat) C.ma_format {
	switch f {
	case S16:
		return C.ma_format_s16
	case S24:
		return C.ma_format_s24
	case S32:
		return C.ma_format_s32
	}
	return C.ma_format_f32
}

func fromMa(f C.int) (SampleFormat, bool) {
	switch f {
	case C.ma_format_s16:
		return S16, true
	case C.ma_format_s24:
		return S24, true
	case C.ma_format_s32:
		return S32, true
	case C.ma_format_f32:
		return F32, true
	}
	return F32, false
}

func bitsOfMa(f C.int) int {
	switch f {
	case C.ma_format_u8:
		return 8
	case C.ma_format_s16:
		return 16
	case C.ma_format_s24:
		return 24
	case C.ma_format_s32, C.ma_format_f32:
		return 32
	}
	return 0
}

func bitsOf(f SampleFormat) int { return bitsOfMa(C.int(toMa(f))) }

// exclusiveCandidates are the exclusive-mode sample formats to offer the device for a source, best first. For
// integer sources every entry carries the samples losslessly (a wider container only pads with zero bits);
// devices differ in which they take.
func exclusiveCandidates(s SourceInfo) []SampleFormat {
	switch {
	case s.IsFloat:
		return []SampleFormat{F32, S32, S24, S16}
	case s.Bits <= 16:
		return []SampleFormat{S16, S32, S24}
	case s.Bits <= 24:
		return []SampleFormat{S32, S24}
	}
	return []SampleFormat{S32}
}

func (b *Backend) ensureContext() bool {
	if b.ctx != nil {
		return true
	}
	null := C.int(0)
	if b.opts.NullDevice {
		null = 1
	}
	b.ctx = C.mp_context_new(null)
	return b.ctx != nil
}

func (b *Backend) idString(id *C.ma_device_id) string {
	raw := C.GoBytes(unsafe.Pointer(id), C.int(C.sizeof_ma_device_id))
	if C.mp_context_is_wasapi(b.ctx) != 0 {
		// Endpoint id string, stable across restarts and re-plugging.
		var u []uint16
		for i := 0; i+1 < len(raw) && i < 128; i += 2 {
			c := binary.LittleEndian.Uint16(raw[i:])
			if c == 0 {
				break
			}
			u = append(u, c)
		}
		return "wasapi:" + string(utf16.Decode(u))
	}
	for len(raw) > 0 && raw[len(raw)-1] == 0 {
		raw = raw[:len(raw)-1]
	}
	return C.GoString(C.mp_context_backend_name(b.ctx)) + ":" + hex.EncodeToString(raw)
}

// deviceInfos returns the context's current device list (valid until the next call).
func (b *Backend) deviceInfos() []*C.ma_device_info {
	if b.ctx == nil {
		return nil
	}
	var infos *C.ma_device_info
	var count C.uint32_t
	if C.mp_get_devices(b.ctx, &infos, &count) == 0 {
		return nil
	}
	out := make([]*C.ma_device_info, int(count))
	for i := range out {
		out[i] = C.mp_devinfo_at(infos, C.uint32_t(i))
	}
	return out
}

func (b *Backend) enumerate() []OutputDevice {
	var out []OutputDevice
	for _, info := range b.deviceInfos() {
		out = append(out, OutputDevice{
			ID:        b.idString((*C.ma_device_id)(unsafe.Pointer(&info.id))),
			Name:      C.GoString(&info.name[0]),
			IsDefault: info.isDefault != 0,
		})
	}
	return out
}

func (b *Backend) query() C.mp_devinfo {
	var info C.mp_devinfo
	C.mp_device_query(b.dev, &info)
	return info
}

func resultText(r C.ma_result) string { return C.GoString(C.ma_result_description(r)) }

func (b *Backend) openDevice(spec deviceSpec) (errMsg string, ok bool) {
	b.closeDevice()
	if !b.ensureContext() {
		return "Cannot initialise audio output", false
	}
	var idp *C.ma_device_id
	if spec.id != "" {
		for _, info := range b.deviceInfos() {
			id := (*C.ma_device_id)(unsafe.Pointer(&info.id))
			if b.idString(id) == spec.id {
				idp = id
				break
			}
		}
		if idp == nil {
			return "the selected output device is not connected", false
		}
	}
	if spec.exclusive && (b.opts.RefuseExclusive ||
		(b.opts.ExclusiveOnlyBits != 0 && bitsOf(spec.format) != b.opts.ExclusiveOnlyBits)) {
		return "the device refused exclusive access", false
	}

	excl := C.int(0)
	if spec.exclusive {
		excl = 1
	}
	r := C.mp_device_open(b.ctx, idp, toMa(spec.format), excl, C.uint32_t(spec.rate), C.uintptr_t(b.handle), &b.dev)
	if r != C.MA_SUCCESS {
		if spec.exclusive {
			return fmt.Sprintf("the device refused exclusive access (%s)", resultText(r)), false
		}
		return fmt.Sprintf("Cannot open the audio output device (%s)", resultText(r)), false
	}
	b.deviceOpen = true
	info := b.query()
	b.rate = int(info.sampleRate)
	b.bpf = 2 * spec.format.BytesPerSample()
	b.rawCopy = spec.exclusive
	b.deviceStopped.Store(false)
	b.rerouted.Store(false)

	b.rb = newRing(b.rate*ringMillis/1000, b.bpf)
	b.rampStep = 1 / (float32(b.rate) * 0.008) // full-scale ramp in 8 ms
	b.devFormat = spec.format
	b.dsp.Configure(b.rate, 16384, 10) // coefficients for this rate; state reset (the callback is not running yet)

	// Audio queued between the callback and the speaker: periods * period size, in device-rate frames.
	scale := 1.0
	if info.internalSampleRate != 0 {
		scale = float64(b.rate) / float64(info.internalSampleRate)
	}
	period := int64(float64(info.internalPeriodSize) * scale)
	b.clock.configure(b.rate, period*int64(info.internalPeriods), period)

	if C.mp_device_start(b.dev) != C.MA_SUCCESS {
		b.closeDevice()
		return "Cannot start the audio device", false
	}
	b.openSpec = spec
	return "", true
}

// closeDevice may only run with the decoder goroutine stopped: it writes into the ring buffer.
func (b *Backend) closeDevice() {
	if b.deviceOpen {
		b.expectStop.Store(true)
		C.mp_device_close(b.dev)
		b.dev = nil
		b.deviceOpen = false
		b.expectStop.Store(false)
		b.deviceStopped.Store(false)
	}
	b.rb = nil
	b.openSpec = deviceSpec{}
}

func (b *Backend) updateOutputFormat() {
	var f OutputFormat
	if b.deviceOpen {
		info := b.query()
		src := b.source
		f.DeviceName = C.GoString(&info.name[0])
		f.SampleRate = int(info.internalSampleRate)
		f.Bits = bitsOfMa(info.internalFormat)
		f.IsFloat = info.internalFormat == C.ma_format_f32
		f.Exclusive = info.shareMode == C.ma_share_mode_exclusive
		if b.dec != nil {
			f.SourceRate = src.Rate
			if !src.IsFloat {
				f.SourceBits = src.Bits
			}
			// Unmodified end to end: integer source, stereo, device opened in the pipeline's own format and rate
			// (so miniaudio's converter is a pass-through), and the callback copying without gain.
			f.BitPerfect = f.Exclusive && b.rawCopy && b.dsp.BypassTarget() && !src.IsFloat && src.Channels == 2 &&
				info.internalFormat == info.format && info.internalChannels == 2 &&
				int(info.internalSampleRate) == src.Rate && b.rate == src.Rate
		}
	}
	b.format = f
	if b.ev.OutputFormatChanged != nil {
		b.ev.OutputFormatChanged()
	}
}

func (b *Backend) stopDecoder() {
	b.quit.Store(true)
	if b.decDone != nil {
		<-b.decDone
		b.decDone = nil
	}
	if b.dec != nil {
		b.dec.Close()
		b.dec = nil
	}
}

func (b *Backend) setState(s State) {
	if s == b.state {
		return
	}
	b.state = s
	if b.ev.StateChanged != nil {
		b.ev.StateChanged(s)
	}
}

func (b *Backend) emitDuration(frames int64) {
	if b.ev.DurationChanged != nil {
		b.ev.DurationChanged(frames)
	}
}

func (b *Backend) emitPosition(frames int64) {
	if b.ev.PositionChanged != nil {
		b.ev.PositionChanged(frames)
	}
}

func (b *Backend) emitError(msg string) {
	if b.ev.Error != nil {
		b.ev.Error(msg)
	}
}

func (b *Backend) emitNotice(msg string) {
	if b.ev.Notice != nil {
		b.ev.Notice(msg)
	}
}

func (b *Backend) emitDevices() {
	if b.ev.DevicesChanged != nil {
		b.ev.DevicesChanged()
	}
}

// Load opens path and prepares it for playback (Stopped, position 0).
func (b *Backend) Load(path string) {
	b.playing.Store(false)
	b.stopDecoder()
	b.setState(Stopped)
	b.atEnd = false
	b.ended.Store(false)
	b.path = ""

	// Open the file first: in exclusive mode its format decides how the device is opened.
	dec, err := OpenDecoder(path, 0, F32)
	if err != nil {
		b.durationFrames = 0
		b.emitDuration(0)
		b.emitError(err.Error())
		return
	}
	src := dec.Source()
	target := b.preferredID
	if b.onFallback {
		target = ""
	}

	opened := false
	if b.exclusivePref {
		// Offer the device each lossless container in turn at the source's own rate, and keep the first it runs
		// natively (driver-reported format == what we send, so miniaudio converts nothing): bit-perfect. A device
		// that takes none of them opens in its own default format instead; then reopen it at exactly that format
		// and rate, so FFmpeg's resampler does the conversion rather than miniaudio's linear one.
		kind := "i"
		if src.IsFloat {
			kind = "f"
		}
		key := target + "|" + strconv.Itoa(src.Rate) + "|" + strconv.Itoa(src.Bits) + kind
		var order []choice
		if c, ok := b.exclusiveBest[key]; ok {
			order = []choice{c} // already probed on this device: go straight to what worked
		} else {
			for _, f := range exclusiveCandidates(src) {
				order = append(order, choice{f, src.Rate})
			}
		}
		why := ""
		var chosen *choice
		var deviceOwn *[2]C.int // format, rate
		tryOpen := func(c choice) bool {
			spec := deviceSpec{target, true, c.format, c.rate}
			if !(b.deviceOpen && b.openSpec == spec) {
				msg, ok := b.openDevice(spec)
				if !ok {
					why = msg
					return false
				}
			}
			info := b.query()
			if info.internalFormat == info.format && int(info.internalSampleRate) == c.rate {
				return true
			}
			if deviceOwn == nil {
				deviceOwn = &[2]C.int{info.internalFormat, C.int(info.internalSampleRate)}
			}
			return false
		}
		for _, c := range order {
			if tryOpen(c) {
				chosen = &c
				break
			}
		}
		if chosen == nil && deviceOwn != nil {
			if f, ok := fromMa(deviceOwn[0]); ok {
				c := choice{f, int(deviceOwn[1])}
				if tryOpen(c) {
					chosen = &c
				}
			}
		}
		opened = chosen != nil
		if opened {
			b.exclusiveBest[key] = *chosen
			dec.SetOutput(b.rate, chosen.format)
		} else {
			delete(b.exclusiveBest, key) // probe again next time
			if !b.exclusiveWarned[key] {
				b.exclusiveWarned[key] = true
				b.emitNotice(fmt.Sprintf("Exclusive mode unavailable: %s. Playing in shared mode.", why))
			}
		}
	}
	if !opened {
		spec := deviceSpec{target, false, F32, 0}
		why := ""
		opened = b.deviceOpen && b.openSpec == spec
		if !opened {
			why, opened = b.openDevice(spec)
		}
		if !opened && target != "" {
			// The chosen device is gone: use the system default until it comes back.
			spec.id = ""
			why, opened = b.openDevice(spec)
			if opened {
				b.onFallback = true
				info := b.query()
				b.emitNotice(fmt.Sprintf("The selected output device is not connected. Playing on %s.",
					C.GoString(&info.name[0])))
			}
		}
		if !opened {
			dec.Close()
			b.durationFrames = 0
			b.emitDuration(0)
			b.emitError(why)
			b.updateOutputFormat()
			return
		}
		dec.SetOutput(b.rate, F32)
	}

	b.source = src
	b.dec = dec
	b.path = path
	b.durationFrames = dec.DurationFrames()

	b.quit.Store(false)
	b.seekTarget.Store(0)
	b.flushGen.Add(1) // makes the new decoder goroutine do the initial flush
	b.decDone = make(chan struct{})
	go b.decodeLoop(dec, b.rb, b.decDone)

	b.tick.Start()
	b.updateOutputFormat()
	b.emitDuration(b.durationFrames)
	b.emitPosition(0)
}

// reopenCurrent re-opens the current track on the device / mode now in effect, keeping position and play state.
func (b *Backend) reopenCurrent() {
	if b.dec == nil || b.path == "" {
		b.stopDecoder()
		b.closeDevice() // the next Load opens the right device
		b.updateOutputFormat()
		return
	}
	was := b.state
	wasAtEnd := b.atEnd
	seconds := 0.0
	if b.rate > 0 {
		seconds = float64(b.PositionFrames()) / float64(b.rate)
	}
	path := b.path

	b.playing.Store(false)
	b.stopDecoder()
	b.closeDevice()
	b.Load(path)
	if b.dec == nil {
		return
	}
	if !wasAtEnd && seconds > 0 {
		b.SeekTo(int64(seconds * float64(b.rate)))
	}
	switch was {
	case Playing:
		b.Play()
	case Paused:
		b.setState(Paused)
	}
}

func (b *Backend) handleDeviceLoss() {
	lost := b.format.DeviceName
	if lost == "" {
		lost = "The output device"
	}
	if b.openSpec.id != "" {
		b.onFallback = true // keep the user's choice; switch back when it returns
	}
	// reopenCurrent pauses delivery, moves to the fallback device and resumes where playback was.
	b.reopenCurrent()
	b.devices = b.enumerate()
	b.emitDevices()
	if b.deviceOpen && b.dec != nil {
		b.emitNotice(fmt.Sprintf("%s was disconnected. Now playing on %s.", lost, b.format.DeviceName))
	} else {
		b.emitNotice(lost + " was disconnected.")
	}
}

// RefreshDevices re-reads the device list now (it is also polled every 2 s) and handles an active device that
// disappeared.
func (b *Backend) RefreshDevices() {
	if !b.ensureContext() {
		return
	}
	if b.deviceStopped.Swap(false) {
		b.handleDeviceLoss()
		return
	}
	list := b.enumerate()
	if !slices.Equal(list, b.devices) {
		b.devices = list
		b.emitDevices()
	}
	present := func(id string) bool {
		return slices.ContainsFunc(list, func(d OutputDevice) bool { return d.ID == id })
	}
	if b.deviceOpen && b.openSpec.id != "" && !present(b.openSpec.id) {
		b.handleDeviceLoss()
	} else if b.onFallback && b.preferredID != "" && present(b.preferredID) {
		b.onFallback = false
		b.reopenCurrent()
		if b.deviceOpen {
			b.emitNotice("Switched back to " + b.format.DeviceName + ".")
		}
	}
}

// SimulateDeviceLoss is a test hook: act as if the active device had just been unplugged.
func (b *Backend) SimulateDeviceLoss() {
	b.deviceStopped.Store(true)
	b.RefreshDevices()
}

func (b *Backend) Play() {
	if b.dec == nil {
		return
	}
	if b.atEnd {
		b.SeekTo(0) // playing again after the track ran out restarts it
	}
	b.playing.Store(true)
	b.setState(Playing)
}

func (b *Backend) Pause() {
	if b.state != Playing {
		return
	}
	b.playing.Store(false)
	b.setState(Paused)
}

// Stop goes to Stopped at position 0, keeping the source.
func (b *Backend) Stop() {
	b.playing.Store(false)
	if b.dec != nil {
		b.SeekTo(0)
	}
	b.setState(Stopped)
}

// Unload stops and drops the source.
func (b *Backend) Unload() {
	b.playing.Store(false)
	b.tick.Stop()
	b.stopDecoder()
	b.path = ""
	b.durationFrames = 0
	b.atEnd = false
	b.setState(Stopped)
	b.updateOutputFormat()
	b.emitDuration(0)
	b.emitPosition(0)
}

func (b *Backend) SeekTo(frames int64) {
	if b.dec == nil {
		return
	}
	frames = max(0, frames)
	if b.durationFrames > 0 {
		frames = min(frames, b.durationFrames)
	}
	b.atEnd = false
	b.ended.Store(false)
	b.seekTarget.Store(frames)
	b.flushGen.Add(1)
	b.emitPosition(frames)
}

// SetGain sets the software volume, 0.0 - 1.0, applied in the signal path (shared mode).
func (b *Backend) SetGain(linear float32) {
	b.gain.Store(math.Float32bits(max(0, min(1, linear))))
}

func (b *Backend) State() State                  { return b.state }
func (b *Backend) SampleRate() int               { return b.rate } // frames per second of position/duration/seek
func (b *Backend) DurationFrames() int64         { return b.durationFrames }
func (b *Backend) UnderrunCount() uint64         { return b.underruns.Load() }
func (b *Backend) OutputDevices() []OutputDevice { return b.devices }
func (b *Backend) OutputDeviceID() string        { return b.preferredID }
func (b *Backend) ExclusiveMode() bool           { return b.exclusivePref }
func (b *Backend) OutputFormat() OutputFormat    { return b.format }

// VolumeLocked: exclusive mode sends samples untouched, so there is no software volume.
func (b *Backend) VolumeLocked() bool {
	if b.deviceOpen {
		return b.rawCopy
	}
	return b.exclusivePref
}

// SetOutputDevice selects the device by id; empty = follow the system default.
func (b *Backend) SetOutputDevice(id string) {
	if id == b.preferredID && !b.onFallback {
		return
	}
	b.preferredID = id
	b.onFallback = false
	b.reopenCurrent()
}

// SetEqualizer applies 10-band EQ settings in the sample path (true bypass when off or flat).
func (b *Backend) SetEqualizer(s eq.Settings) {
	wasFlat := b.dsp.BypassTarget()
	b.dsp.SetSettings(s) // lock-free hand-over to the callback, which ramps to it
	if wasFlat != b.dsp.BypassTarget() {
		b.updateOutputFormat() // bit-perfect only while the equalizer is off or flat
	}
}

// SetExclusiveMode: bypass the OS mixer and play each file at its own rate and sample format (bit-perfect where
// the device supports it). Software volume is disabled while it is active.
func (b *Backend) SetExclusiveMode(on bool) {
	if on == b.exclusivePref {
		return
	}
	b.exclusivePref = on
	clear(b.exclusiveWarned)
	b.reopenCurrent()
}

// ClockSeconds is the continuously advancing playback position, meant to be polled at display rate (frame
// animation, lyric selection). It interpolates between device callbacks. Single reader.
func (b *Backend) ClockSeconds() float64 {
	if b.rate <= 0 {
		return 0
	}
	// A seek that the audio thread has not acknowledged yet: the requested position is already "now".
	if b.consumerAck.Load() != b.flushGen.Load() {
		return float64(b.seekTarget.Load()) / float64(b.rate)
	}
	return b.clock.positionSeconds(nowNs())
}

func (b *Backend) PositionFrames() int64 {
	// Between a seek request and the callback draining the old audio, report the requested position.
	if b.consumerAck.Load() != b.flushGen.Load() {
		return b.seekTarget.Load()
	}
	return b.pos.Load()
}

func (b *Backend) onTick() {
	if b.deviceStopped.Load() {
		b.RefreshDevices() // handles the loss
		return
	}
	if b.rerouted.Swap(false) {
		b.updateOutputFormat() // miniaudio followed a default-device change; the format may differ
	}
	if b.dec == nil {
		return
	}
	if b.ended.Swap(false) {
		b.handleEnd()
		return
	}
	if b.state == Playing {
		b.emitPosition(b.PositionFrames())
	}
}

func (b *Backend) handleEnd() {
	b.playing.Store(false)
	b.atEnd = true
	b.setState(Stopped)
	b.emitPosition(b.durationFrames)
	if b.ev.EndOfStream != nil {
		b.ev.EndOfStream()
	}
}
