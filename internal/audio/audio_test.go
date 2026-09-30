package audio

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"musicplayer/internal/core"
	"musicplayer/internal/eq"
	"musicplayer/internal/lyrics"
)

// writeWav writes a PCM WAV with interleaved stereo samples of the given bit depth.
func writeWav(t *testing.T, path string, rate, bits int, samples []int32) string {
	t.Helper()
	frames := uint32(len(samples) / 2)
	nb := uint16(bits / 8)
	var b bytes.Buffer
	le32 := func(v uint32) { binary.Write(&b, binary.LittleEndian, v) }
	le16 := func(v uint16) { binary.Write(&b, binary.LittleEndian, v) }
	b.WriteString("RIFF")
	le32(36 + frames*2*uint32(nb))
	b.WriteString("WAVEfmt ")
	le32(16)
	le16(1)
	le16(2)
	le32(uint32(rate))
	le32(uint32(rate) * 2 * uint32(nb))
	le16(2 * nb)
	le16(uint16(bits))
	b.WriteString("data")
	le32(frames * 2 * uint32(nb))
	for _, v := range samples {
		for i := 0; i < int(nb); i++ {
			b.WriteByte(byte(uint32(v) >> (8 * i)))
		}
	}
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// counterWav: 48 kHz, 16-bit stereo. The left channel is a ramp so every frame within 65536 is unique.
func counterWav(t *testing.T, path string, seconds float64) string {
	frames := int(48000 * seconds)
	s := make([]int32, frames*2)
	for i := 0; i < frames; i++ {
		v := int32(i%65536) - 32768
		s[2*i] = v
		s[2*i+1] = -max(v, -32767)
	}
	return writeWav(t, path, 48000, 16, s)
}

func decodeAll(d *Decoder) []float32 {
	var out []float32
	chunk := make([]byte, 4096*8)
	for {
		n := d.Read(chunk)
		if n <= 0 {
			return out
		}
		out = append(out, floats(chunk, n)...)
	}
}

func ffmpegTool() string {
	if p, err := exec.LookPath("ffmpeg"); err == nil {
		return p
	}
	return ""
}

func checkSeek(t *testing.T, path string, bitExact bool) {
	d, err := OpenDecoder(path, 48000, F32)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	ref := decodeAll(d)
	total := int64(len(ref) / 2)
	if diff := total - d.DurationFrames(); diff > 3000 || diff < -3000 {
		t.Errorf("%s: decoded %d frames, duration %d", filepath.Base(path), total, d.DurationFrames())
	}
	got := make([]byte, 2048*8)
	for _, target := range []int64{0, 1, 1000, 47999, 48000, 100003, 123456, 200000, 287000} {
		if target+4096 >= total {
			continue
		}
		if !d.SeekTo(target) || d.PositionFrames() != target {
			t.Fatalf("seek to %d", target)
		}
		if d.Read(got) != 2048 {
			t.Fatalf("short read after seek to %d", target)
		}
		g := floats(got, 2048)
		if bitExact {
			for i := range g {
				if g[i] != ref[int(target)*2+i] {
					t.Fatalf("%s: seek to %d differs from continuous decode", filepath.Base(path), target)
				}
			}
			continue
		}
		best, bestScore := 0, math.Inf(-1)
		for lag := -1500; lag <= 1500; lag++ {
			if target+int64(lag) < 0 || target+int64(lag)+2048 >= total {
				continue
			}
			s := 0.0
			for i := 64; i < 1984; i++ {
				s += float64(g[i*2]) * float64(ref[(int(target)+lag+i)*2])
			}
			if s > bestScore {
				bestScore, best = s, lag
			}
		}
		if best < -1 || best > 1 {
			t.Errorf("%s: target %d lag %d", filepath.Base(path), target, best)
		}
	}
}

func TestDecoderSeek(t *testing.T) {
	dir := t.TempDir()
	checkSeek(t, counterWav(t, filepath.Join(dir, "counter.wav"), 6), true)

	ff := ffmpegTool()
	if ff == "" {
		t.Log("ffmpeg not found: compressed fixtures skipped")
		return
	}
	src := "anoisesrc=d=6:c=white:r=48000:a=0.4:seed=7"
	for _, c := range []struct {
		name     string
		args     []string
		bitExact bool
	}{
		{"noise.flac", nil, true},
		{"noise.mp3", []string{"-b:a", "256k"}, false},
		{"noise.m4a", []string{"-c:a", "aac", "-b:a", "256k"}, false},
		{"noise.ogg", []string{"-c:a", "libvorbis", "-b:a", "256k"}, false},
	} {
		out := filepath.Join(dir, c.name)
		args := append([]string{"-loglevel", "error", "-y", "-f", "lavfi", "-i", src, "-ac", "2"}, c.args...)
		if err := exec.Command(ff, append(args, out)...).Run(); err != nil {
			t.Logf("%s: encoder unavailable (%v), skipped", c.name, err)
			continue
		}
		checkSeek(t, out, c.bitExact)
	}
}

// harness runs a loop for a backend on the null device. Backend methods are called through do (with the lock).
type harness struct {
	t    *testing.T
	loop *core.Loop
	b    *Backend
	ev   struct {
		ended   atomic.Int32
		notices []string
	}
}

func newHarness(t *testing.T, opts Options) *harness {
	h := &harness{t: t, loop: core.NewLoop(nil)}
	go h.loop.Run()
	opts.NullDevice = true
	h.loop.Do(func() {
		h.b = NewBackend(h.loop, opts, Events{
			EndOfStream: func() { h.ev.ended.Add(1) },
			Notice:      func(m string) { h.ev.notices = append(h.ev.notices, m) },
			Error:       func(m string) { t.Logf("backend error: %s", m) },
		})
	})
	t.Cleanup(func() {
		h.loop.Do(h.b.Close)
		h.loop.Stop()
	})
	return h
}

func (h *harness) do(f func(b *Backend)) { h.loop.Do(func() { f(h.b) }) }

func (h *harness) position() int64 {
	var p int64
	h.do(func(b *Backend) { p = b.PositionFrames() })
	return p
}

func TestPipeline(t *testing.T) {
	dir := t.TempDir()
	wav := counterWav(t, filepath.Join(dir, "six.wav"), 6)
	short := counterWav(t, filepath.Join(dir, "one.wav"), 1)
	h := newHarness(t, Options{})

	var rate float64
	h.do(func(b *Backend) {
		b.SetGain(0.8)
		b.Load(wav)
		rate = float64(b.SampleRate())
		if rate <= 0 || math.Abs(float64(b.DurationFrames())/rate-6) > 0.05 || b.State() != Stopped {
			t.Fatalf("load: rate %v, duration %d, state %v", rate, b.DurationFrames(), b.State())
		}
		b.Play()
	})
	// Real-time pacing: the position follows the wall clock.
	start := time.Now()
	time.Sleep(2500 * time.Millisecond)
	pos1 := float64(h.position()) / rate * 1000
	if el := float64(time.Since(start).Milliseconds()); math.Abs(pos1-el) > 250 {
		t.Errorf("position %.0f ms vs elapsed %.0f ms", pos1, el)
	}

	// Pause holds the position.
	h.do(func(b *Backend) { b.Pause() })
	time.Sleep(150 * time.Millisecond) // let the fade-out finish
	held := h.position()
	time.Sleep(400 * time.Millisecond)
	if d := h.position() - held; d > int64(rate*0.01) || d < -int64(rate*0.01) {
		t.Error("position moved while paused")
	}

	// A seek is immediately visible, then playback continues from there.
	target := int64(4 * rate)
	h.do(func(b *Backend) {
		b.SeekTo(target)
		if b.PositionFrames() != target {
			t.Error("seek not visible at once")
		}
		b.Play()
	})
	time.Sleep(600 * time.Millisecond)
	if p := float64(h.position()) / rate; p < 4.3 || p > 4.9 {
		t.Errorf("position after seek+play: %.3f s", p)
	}

	// Rapid seeks must not wedge the handshake.
	for i := 0; i < 20; i++ {
		h.do(func(b *Backend) { b.SeekTo(int64((0.5 + 0.1*float64(i)) * rate)) })
		time.Sleep(15 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	if p := float64(h.position()); p < rate*2 || p > rate*3.5 {
		t.Errorf("after rapid seeks: %.0f", p)
	}

	// End of stream on a short file, then replay.
	h.do(func(b *Backend) { b.Load(short); b.Play() })
	time.Sleep(2200 * time.Millisecond)
	if n := h.ev.ended.Load(); n != 1 {
		t.Errorf("end of stream count %d", n)
	}
	h.do(func(b *Backend) {
		if b.State() != Stopped {
			t.Error("not stopped at the end")
		}
		b.Play() // restarts from the beginning
	})
	time.Sleep(300 * time.Millisecond)
	h.do(func(b *Backend) {
		if b.State() != Playing || float64(b.PositionFrames()) > rate*0.6 {
			t.Errorf("replay: state %v position %d", b.State(), b.PositionFrames())
		}
		b.Unload()
		if b.DurationFrames() != 0 {
			t.Error("duration after unload")
		}
		if b.UnderrunCount() != 0 {
			t.Errorf("underruns %d", b.UnderrunCount())
		}
	})
}

func TestClock(t *testing.T) {
	dir := t.TempDir()
	// 10 s, a click at the start of every LRC line (every 400 ms, offset 200 ms).
	const rate = 48000
	frames := 10 * rate
	s := make([]int32, frames*2)
	var clicks []int64
	for ms := int64(200); ms < 10000-50; ms += 400 {
		clicks = append(clicks, ms)
		st := int(ms * rate / 1000)
		for i := 0; i < rate/200; i++ {
			v := int32(20000)
			if i%2 == 0 {
				v = -20000
			}
			s[2*(st+i)], s[2*(st+i)+1] = v, v
		}
	}
	clickWav := writeWav(t, filepath.Join(dir, "click.wav"), rate, 16, s)
	counter := counterWav(t, filepath.Join(dir, "counter.wav"), 30)

	h := newHarness(t, Options{})
	clockMs := func() float64 {
		var c float64
		h.do(func(b *Backend) { c = b.ClockSeconds() * 1000 })
		return c
	}
	h.do(func(b *Backend) { b.Load(counter); b.Play() })
	time.Sleep(500 * time.Millisecond) // warm-up

	// Smoothness at 60 Hz: peak deviation from a straight line, and monotonic.
	type sample struct{ wall, pos float64 }
	var run []sample
	t0 := time.Now()
	for time.Since(t0) < 2*time.Second {
		run = append(run, sample{float64(time.Since(t0).Nanoseconds()) / 1e6, clockMs()})
		time.Sleep(16 * time.Millisecond)
	}
	n := float64(len(run))
	var sx, sy, sxx, sxy float64
	for _, p := range run {
		sx += p.wall
		sy += p.pos
		sxx += p.wall * p.wall
		sxy += p.wall * p.pos
	}
	slope := (n*sxy - sx*sy) / (n*sxx - sx*sx)
	icpt := (sy - slope*sx) / n
	peak := 0.0
	for i, p := range run {
		peak = math.Max(peak, math.Abs(p.pos-(slope*p.wall+icpt)))
		if i > 0 && p.pos < run[i-1].pos {
			t.Error("clock went backwards")
		}
	}
	t.Logf("clock: peak deviation %.2f ms, slope %.4f", peak, slope)
	if peak > 8 || math.Abs(slope-1) > 0.005 {
		t.Errorf("clock not smooth: peak %.2f ms, slope %.4f", peak, slope)
	}

	// An explicit seek: immediately at the target, never below it afterwards.
	h.do(func(b *Backend) { b.SeekTo(3 * rate) })
	if c := clockMs(); math.Abs(c-3000) > 1 {
		t.Errorf("clock after seek %.1f ms", c)
	}
	prev := 3000.0
	for i := 0; i < 100; i++ {
		time.Sleep(6 * time.Millisecond)
		c := clockMs()
		if c < prev-1e-6 || c < 3000-1e-6 {
			t.Fatalf("clock %.3f after %.3f following a seek to 3 s", c, prev)
		}
		prev = c
	}

	// A new track starts a new run, and each lyric line switches at the same offset from its click.
	h.do(func(b *Backend) { b.Load(clickWav) })
	if c := clockMs(); c > 50 {
		t.Errorf("clock carried %.1f ms into the next track", c)
	}
	var lines []lyrics.Line
	for _, ms := range clicks {
		lines = append(lines, lyrics.Line{TimestampMs: ms})
	}
	h.do(func(b *Backend) { b.Play() })
	wall := time.Now()
	active := -1
	lo, hi := math.Inf(1), math.Inf(-1)
	switches := 0
	for time.Since(wall) < 9500*time.Millisecond {
		idx := lyrics.ActiveIndexAt(lines, int64(clockMs()))
		if idx != active {
			active = idx
			if idx >= 0 {
				off := float64(time.Since(wall).Nanoseconds())/1e6 - float64(lines[idx].TimestampMs)
				lo, hi = math.Min(lo, off), math.Max(hi, off)
				switches++
			}
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Logf("lyric switches: %d, spread %.2f ms", switches, hi-lo)
	if switches < 20 || hi-lo > 20 {
		t.Errorf("lyric switching: %d switches, spread %.2f ms", switches, hi-lo)
	}
}

func pcm24Pattern(frames int) []int32 {
	s := make([]int32, frames*2)
	for i := 0; i < frames; i++ {
		a := uint32(i) * 2654435761
		s[2*i] = int32((a>>8)&0xFFFFFF) - 8388608
		s[2*i+1] = int32((a>>4)&0xFFFFFF) - 8388608
	}
	s[0], s[1] = 8388607, -8388608 // frame 0 is distinctive: used to find the start in the capture
	for i := 1000; i < frames; i += 4801 {
		s[2*i], s[2*i+1] = 8388607, -8388608
	}
	return s
}

// capture records everything the callback hands to the device, without allocating on the audio thread.
type capture struct {
	buf  []byte
	used atomic.Int64
	bpf  atomic.Int32
}

func (c *capture) tap(frames []byte, count, bpf int) {
	c.bpf.Store(int32(bpf))
	at := c.used.Load()
	n := int64(count * bpf)
	if at+n <= int64(len(c.buf)) {
		copy(c.buf[at:], frames[:n])
		c.used.Store(at + n)
	}
}

func (c *capture) contains(expected []byte, bpf int) bool {
	data := c.buf[:c.used.Load()]
	for at := 0; at+len(expected) <= len(data); at += bpf {
		if bytes.Equal(data[at:at+bpf], expected[:bpf]) {
			return bytes.Equal(data[at:at+len(expected)], expected)
		}
	}
	return false
}

func checkBitPerfect(t *testing.T, label, file string, rate int, expected []byte, expectedBpf, sourceBits,
	deviceOnlyBits int, eqOnFlat bool) {
	t.Run(label, func(t *testing.T) {
		c := &capture{buf: make([]byte, rate*4*expectedBpf)} // 4 s of device output
		h := newHarness(t, Options{ExclusiveOnlyBits: deviceOnlyBits, Tap: c.tap})
		h.do(func(b *Backend) {
			b.SetExclusiveMode(true)
			b.SetGain(0.3) // must be ignored: exclusive mode sends samples untouched
			if eqOnFlat {
				b.SetEqualizer(eq.Settings{Enabled: true}) // switched on, every band at 0 dB: true bypass
			}
			b.Load(file)
			f := b.OutputFormat()
			if b.SampleRate() != rate || !f.Exclusive || !f.BitPerfect || f.SampleRate != rate ||
				f.SourceRate != rate || f.SourceBits != sourceBits || !b.VolumeLocked() {
				t.Fatalf("rate %d, format %+v", b.SampleRate(), f)
			}
			b.Play()
		})
		deadline := time.Now().Add(6 * time.Second)
		for h.ev.ended.Load() == 0 && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if h.ev.ended.Load() == 0 {
			t.Fatal("playback did not reach the end")
		}
		if bpf := int(c.bpf.Load()); bpf != expectedBpf {
			t.Fatalf("%d bytes per frame on the device", bpf)
		}
		if !c.contains(expected, expectedBpf) {
			t.Fatal("device output differs from the source")
		}
	})
}

func TestExclusive(t *testing.T) {
	dir := t.TempDir()
	// 24-bit / 96 kHz: carried as 24 valid bits in 32-bit samples (value << 8).
	const rate = 96000
	s24 := pcm24Pattern(rate * 2)
	wav24 := writeWav(t, filepath.Join(dir, "s24_96k.wav"), rate, 24, s24)
	var exp32, expPacked []byte
	for _, v := range s24 {
		exp32 = binary.LittleEndian.AppendUint32(exp32, uint32(v)<<8)
		expPacked = append(expPacked, byte(v), byte(v>>8), byte(v>>16))
	}
	checkBitPerfect(t, "24-bit 96 kHz WAV", wav24, rate, exp32, 8, 24, 0, false)
	checkBitPerfect(t, "24-bit 96 kHz WAV, EQ on but flat", wav24, rate, exp32, 8, 24, 0, true)
	// A device that only takes packed 24-bit in exclusive mode: sent as 3-byte samples.
	checkBitPerfect(t, "24-bit 96 kHz WAV, packed-24 device", wav24, rate, expPacked, 6, 24, 24, false)

	// 16-bit source on a 32-bit-only device: zero-padded (value << 16), still lossless; and on a normal device.
	s16 := make([]int32, 48000*2)
	for i := range s16 {
		s16[i] = int32(int16(uint32(i) * 40503))
	}
	s16[0], s16[1] = 32767, -32768
	wav16 := writeWav(t, filepath.Join(dir, "s16_48k.wav"), 48000, 16, s16)
	var expPad, exp16 []byte
	for _, v := range s16 {
		expPad = binary.LittleEndian.AppendUint32(expPad, uint32(v)<<16)
		exp16 = binary.LittleEndian.AppendUint16(exp16, uint16(v))
	}
	checkBitPerfect(t, "16-bit 48 kHz WAV, 32-bit-only device", wav16, 48000, expPad, 8, 16, 32, false)
	checkBitPerfect(t, "16-bit 48 kHz WAV", wav16, 48000, exp16, 4, 16, 0, false)

	if ff := ffmpegTool(); ff != "" {
		flac := filepath.Join(dir, "s24_96k.flac")
		if exec.Command(ff, "-loglevel", "error", "-y", "-i", wav24, "-c:a", "flac", flac).Run() == nil {
			checkBitPerfect(t, "24-bit 96 kHz FLAC", flac, rate, exp32, 8, 24, 0, false)
		}
	}

	// A device that refuses exclusive access: shared mode, with one message, and playback still works.
	h := newHarness(t, Options{RefuseExclusive: true})
	h.do(func(b *Backend) {
		b.SetExclusiveMode(true)
		b.Load(wav24)
		if len(h.ev.notices) != 1 || !strings.Contains(h.ev.notices[0], "Exclusive mode unavailable") {
			t.Errorf("fallback notices: %q", h.ev.notices)
		}
		f := b.OutputFormat()
		if f.Exclusive || f.BitPerfect || !f.IsFloat || b.VolumeLocked() {
			t.Errorf("fallback should be shared float: %+v", f)
		}
		b.Play()
	})
	time.Sleep(400 * time.Millisecond)
	h.do(func(b *Backend) {
		if b.PositionFrames() < int64(b.SampleRate()/10) {
			t.Error("no playback after the fallback")
		}
		b.Load(wav24) // same device and format: not reported again
		if len(h.ev.notices) != 1 {
			t.Errorf("fallback reported %d times", len(h.ev.notices))
		}
	})
}

func TestDevices(t *testing.T) {
	wav := counterWav(t, filepath.Join(t.TempDir(), "six.wav"), 6)
	h := newHarness(t, Options{})
	var list []OutputDevice
	h.do(func(b *Backend) {
		list = b.OutputDevices()
		if len(list) == 0 {
			t.Fatal("no devices enumerated")
		}
		b.RefreshDevices()
		if len(b.OutputDevices()) != len(list) || b.OutputDevices()[0] != list[0] {
			t.Error("device ids are not stable across enumerations")
		}
		// An explicit device, then losing it mid-playback.
		b.SetOutputDevice(list[0].ID)
		b.Load(wav)
		if b.OutputFormat().DeviceName != list[0].Name {
			t.Errorf("device name %q", b.OutputFormat().DeviceName)
		}
		b.Play()
	})
	time.Sleep(500 * time.Millisecond)
	var before int64
	var rate int
	h.do(func(b *Backend) {
		before, rate = b.PositionFrames(), b.SampleRate()
		b.SimulateDeviceLoss()
		if len(h.ev.notices) != 1 || !strings.Contains(h.ev.notices[0], "disconnected") {
			t.Errorf("loss notices: %q", h.ev.notices)
		}
		if b.State() != Playing {
			t.Error("not playing after the device was lost")
		}
		if d := b.PositionFrames() - before; d > int64(rate/4) || d < -int64(rate/4) {
			t.Error("position jumped on device loss")
		}
	})
	time.Sleep(500 * time.Millisecond)
	if h.position() < before+int64(rate/4) {
		t.Error("playback did not resume on the fallback device")
	}
	// A remembered device that is not connected: plays on the default and says so.
	h.do(func(b *Backend) {
		h.ev.notices = nil
		b.SetOutputDevice("wasapi:{not-connected}")
		if b.State() != Playing {
			t.Error("not playing on the default device")
		}
		if len(h.ev.notices) != 1 || !strings.Contains(h.ev.notices[0], "not connected") {
			t.Errorf("missing-device notices: %q", h.ev.notices)
		}
		if b.OutputDeviceID() != "wasapi:{not-connected}" {
			t.Error("the user's choice was not kept")
		}
		b.Unload()
	})
}
