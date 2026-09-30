// Package eq holds the 10-band graphic equalizer settings, the RBJ ("Audio EQ Cookbook") peaking filter design
// and the real-time DSP chain that applies them.
package eq

import (
	"math"
	"math/cmplx"
)

const (
	Bands     = 10
	Q         = 1.4142135623730951 // one-octave bandwidth
	MaxGainDb = 12.0
)

// Frequencies are the ISO octave centres.
var Frequencies = [Bands]float64{31.25, 62.5, 125, 250, 500, 1000, 2000, 4000, 8000, 16000}

type Settings struct {
	Enabled  bool
	PreampDb float64
	GainsDb  [Bands]float64
}

// IsFlat reports true bypass: nothing to do (off, or every gain and the preamp at exactly 0 dB).
func (s Settings) IsFlat() bool {
	if !s.Enabled {
		return true
	}
	if s.PreampDb != 0 {
		return false
	}
	for _, g := range s.GainsDb {
		if g != 0 {
			return false
		}
	}
	return true
}

// Clamped returns s with the preamp and every gain limited to +-MaxGainDb.
func (s Settings) Clamped() Settings {
	s.PreampDb = clampDb(s.PreampDb)
	for i := range s.GainsDb {
		s.GainsDb[i] = clampDb(s.GainsDb[i])
	}
	return s
}

func clampDb(v float64) float64 { return math.Max(-MaxGainDb, math.Min(MaxGainDb, v)) }

// Biquad: y[n] = b0 x[n] + b1 x[n-1] + b2 x[n-2] - a1 y[n-1] - a2 y[n-2]   (normalised: a0 = 1)
type Biquad struct {
	B0, B1, B2, A1, A2 float64
}

// Identity is the pass-through filter.
var Identity = Biquad{B0: 1}

func (b Biquad) IsIdentity() bool { return b == Identity }

// Peaking is the RBJ peaking EQ. 0 dB, or a centre frequency too close to Nyquist to be meaningful, gives the
// exact identity.
func Peaking(sampleRate, f0, q, gainDb float64) Biquad {
	if gainDb == 0 || sampleRate <= 0 || f0 >= 0.45*sampleRate {
		return Identity
	}
	A := math.Pow(10, gainDb/40)
	w0 := 2 * math.Pi * f0 / sampleRate
	alpha := math.Sin(w0) / (2 * q)
	c := math.Cos(w0)
	a0 := 1 + alpha/A
	return Biquad{(1 + alpha*A) / a0, (-2 * c) / a0, (1 - alpha*A) / a0, (-2 * c) / a0, (1 - alpha/A) / a0}
}

// MagnitudeDb is |H(e^jw)| of one biquad at frequency f, in dB (the analytical response).
func MagnitudeDb(b Biquad, sampleRate, f float64) float64 {
	w := 2 * math.Pi * f / sampleRate
	z1, z2 := cmplx.Rect(1, -w), cmplx.Rect(1, -2*w)
	h := (complex(b.B0, 0) + complex(b.B1, 0)*z1 + complex(b.B2, 0)*z2) /
		(1 + complex(b.A1, 0)*z1 + complex(b.A2, 0)*z2)
	return 20 * math.Log10(cmplx.Abs(h))
}
