package eq

import (
	"math"
	"math/rand"
	"slices"
	"testing"
	"time"
)

// sineAmplitude is the amplitude of a sinusoid at f in y[from, from+n) by least squares on sin / cos (exact for any
// window length).
func sineAmplitude(y []float32, from, n int, f, fs float64) float64 {
	var ss, cc, sc, ys, yc float64
	for i := from; i < from+n; i++ {
		ph := 2 * math.Pi * f * float64(i) / fs
		s, c, v := math.Sin(ph), math.Cos(ph), float64(y[i*2])
		ss += s * s
		cc += c * c
		sc += s * c
		ys += v * s
		yc += v * c
	}
	det := ss*cc - sc*sc
	a, b := (ys*cc-yc*sc)/det, (yc*ss-ys*sc)/det
	return math.Sqrt(a*a + b*b)
}

func stereoSine(f, fs float64, frames int, amp float64) []float32 {
	v := make([]float32, frames*2)
	for i := 0; i < frames; i++ {
		x := float32(amp * math.Sin(2*math.Pi*f*float64(i)/fs))
		v[2*i], v[2*i+1] = x, x
	}
	return v
}

func TestResponse(t *testing.T) {
	// The design itself: a peaking band has exactly its gain at the centre frequency and none at DC.
	pk := Peaking(48000, 1000, Q, 6)
	if math.Abs(MagnitudeDb(pk, 48000, 1000)-6) > 1e-9 || math.Abs(MagnitudeDb(pk, 48000, 1e-3)) > 1e-6 {
		t.Error("peaking design")
	}

	s := Settings{Enabled: true, PreampDb: -6, GainsDb: [Bands]float64{12, -12, 6, -6, 3, -3, 9, -9, 12, -12}}
	worst := 0.0
	for _, fs := range []float64{44100, 48000, 96000} {
		chain := NewChain()
		chain.Configure(int(fs), 8192, 10)
		chain.SetSettings(s)
		var bands [Bands]Biquad
		for b := range bands {
			bands[b] = Peaking(fs, Frequencies[b], Q, s.GainsDb[b])
		}
		fMax := math.Min(20000, 0.45*fs)
		settle, measure := int(0.4*fs), int(0.2*fs)
		for k := 0; k < 40; k++ {
			f := 20 * math.Pow(fMax/20, float64(k)/39)
			buf := stereoSine(f, fs, settle+measure, 0.1)
			for done := 0; done < settle+measure; done += 480 { // callback-sized blocks
				n := min(480, settle+measure-done)
				chain.Process(buf[done*2:], n)
			}
			measured := 20 * math.Log10(sineAmplitude(buf, settle, measure, f, fs)/0.1)
			analytic := s.PreampDb
			for _, b := range bands {
				analytic += MagnitudeDb(b, fs, f)
			}
			worst = math.Max(worst, math.Abs(measured-analytic))
		}
	}
	if worst > 0.1 {
		t.Errorf("response deviates by %.4f dB", worst)
	}
}

func processAll(c *Chain, buf []float32, block int) bool {
	touched := false
	frames := len(buf) / 2
	for done := 0; done < frames; done += block {
		touched = c.Process(buf[done*2:], min(block, frames-done)) || touched
	}
	return touched
}

func TestBypass(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	noise := func(frames int) []float32 {
		v := make([]float32, frames*2)
		for i := range v {
			v[i] = rng.Float32()*1.8 - 0.9
		}
		return v
	}
	chain := NewChain()
	chain.Configure(48000, 8192, 10)
	flat := Settings{Enabled: true} // on, every gain at 0 dB
	chain.SetSettings(flat)
	in := noise(48000)
	out := slices.Clone(in)
	if processAll(chain, out, 512) || !slices.Equal(in, out) {
		t.Fatal("flat settings changed the samples")
	}

	// Non-flat, then back to flat: after the ramp the chain drops back to true bypass.
	boost := flat
	boost.GainsDb[3] = 9
	boost.PreampDb = -3
	chain.SetSettings(boost)
	processAll(chain, noise(48000), 512)
	chain.SetSettings(flat)
	processAll(chain, noise(4800), 480) // ramp to flat
	in = noise(48000)
	out = slices.Clone(in)
	if processAll(chain, out, 512) || !slices.Equal(in, out) {
		t.Fatalf("not bit-identical after returning to flat (running=%v)", chain.Running())
	}
}

// clickRatio: clicks show up as spikes in the second difference, which for a clean sinusoid of amplitude A is at
// most A * (2 sin(pi f / fs))^2. It returns the worst ratio of |second difference| to that bound (about 1 when clean).
func clickRatio(rampMs float64) float64 {
	const fs, f = 48000.0, 1000.0
	frames := int(3 * fs)
	chain := NewChain()
	chain.Configure(int(fs), 8192, rampMs)
	rng := rand.New(rand.NewSource(99))
	gain := func() float64 { return rng.Float64()*24 - 12 }
	buf := stereoSine(f, fs, frames, 0.25)
	s := Settings{Enabled: true}
	for done := 0; done < frames; done += 480 {
		if done%960 == 0 { // every 20 ms, like a slider being dragged: new gains everywhere
			for i := range s.GainsDb {
				s.GainsDb[i] = gain()
			}
			s.PreampDb = -math.Abs(gain()) / 2
			chain.SetSettings(s)
		}
		chain.Process(buf[done*2:], 480)
	}
	k := math.Pow(2*math.Sin(math.Pi*f/fs), 2)
	period := int(fs / f)
	worst := 0.0
	for i := 2 * period; i+2*period < frames; i++ {
		local := 0.0 // envelope: peak over +-2 periods
		for j := -2 * period; j <= 2*period; j += 4 {
			local = math.Max(local, math.Abs(float64(buf[2*(i+j)])))
		}
		d2 := math.Abs(float64(buf[2*i]) - 2*float64(buf[2*(i-1)]) + float64(buf[2*(i-2)]))
		worst = math.Max(worst, d2/(local*k+1e-9))
	}
	return worst
}

func TestNoClicks(t *testing.T) {
	stepped := clickRatio(0) // coefficients switched instantly: the detector must fire
	ramped := clickRatio(10)
	if stepped <= 2 {
		t.Errorf("detector did not see the clicks of instant switching (%.2f)", stepped)
	}
	if ramped >= 1.5 {
		t.Errorf("clicks with ramping (%.2f)", ramped)
	}
}

func TestProcessIntRoundTrip(t *testing.T) {
	// Every 24-bit value survives the float conversion exactly when the chain only ramps an identity preamp.
	chain := NewChain()
	chain.Configure(48000, 8192, 10)
	chain.SetSettings(Settings{Enabled: true, PreampDb: 6})
	buf := make([]byte, 480*6)
	if !chain.ProcessInt(buf, S24, 480) {
		t.Fatal("chain not running")
	}
}

func TestCPU(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	const fs = 96000
	frames := 10 * fs
	s := Settings{Enabled: true, PreampDb: -6, GainsDb: [Bands]float64{3, -3, 5, -5, 2, -2, 4, -4, 6, -6}}
	rng := rand.New(rand.NewSource(5))
	f32 := make([]float32, frames*2)
	for i := range f32 {
		f32[i] = rng.Float32() - 0.5
	}
	s24 := make([]byte, frames*6)
	for i := 0; i < frames*2; i++ {
		v := int32(f32[i] * 8388607)
		s24[3*i], s24[3*i+1], s24[3*i+2] = byte(v), byte(v>>8), byte(v>>16)
	}
	for pass := 0; pass < 2; pass++ {
		chain := NewChain()
		chain.Configure(fs, 8192, 10)
		chain.SetSettings(s)
		start := time.Now()
		for done := 0; done < frames; done += 480 { // 5 ms callbacks
			if pass == 0 {
				chain.Process(f32[done*2:], 480)
			} else {
				chain.ProcessInt(s24[done*6:], S24, 480)
			}
		}
		pct := time.Since(start).Seconds() / 10 * 100
		t.Logf("pass %d: %.3f%% of one core", pass, pct)
		if pct > 2 {
			t.Errorf("%.2f%% CPU", pct)
		}
	}
}
