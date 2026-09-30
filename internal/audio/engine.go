package audio

import (
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"musicplayer/internal/core"
	"musicplayer/internal/eq"
	"musicplayer/internal/formats"
	"musicplayer/internal/settings"
)

// EngineEvents are the engine's notifications, called on the loop. Any may be nil.
type EngineEvents struct {
	PositionChanged     func(ms int64)
	DurationChanged     func(ms int64)
	StateChanged        func(State)
	TrackFinished       func()
	Error               func(msg string)
	Notice              func(msg string)
	DevicesChanged      func()
	OutputFormatChanged func()
	EqualizerChanged    func()
	EqPresetsChanged    func()
}

type preset struct {
	preampDb float64
	gainsDb  [eq.Bands]float64
}

var builtinPresets = []struct {
	name string
	preset
}{
	{"Flat", preset{0, [eq.Bands]float64{0, 0, 0, 0, 0, 0, 0, 0, 0, 0}}},
	{"Bass Boost", preset{-4, [eq.Bands]float64{6, 5, 4, 2, 0, 0, 0, 0, 0, 0}}},
	{"Treble Boost", preset{-4, [eq.Bands]float64{0, 0, 0, 0, 0, 1, 2, 4, 5, 6}}},
	{"Vocal", preset{-2, [eq.Bands]float64{-2, -2, -1, 1, 3, 3, 2, 1, 0, -1}}},
	{"Loudness", preset{-4, [eq.Bands]float64{5, 4, 2, 0, -1, -1, 0, 1, 3, 4}}},
	{"Rock", preset{-3, [eq.Bands]float64{4, 3, 2, 0, -1, 0, 1, 2, 3, 3}}},
}

// Engine is the playback facade used by the rest of the app. Decoding and output live in the Backend (FFmpeg
// decoder goroutine -> PCM ring buffer -> miniaudio device callback). The engine converts backend frames to
// milliseconds, gates loading through the supported formats, and remembers the output device, exclusive mode
// and equalizer across restarts. Methods must be called on the loop.
type Engine struct {
	b           *Backend
	ev          EngineEvents
	store       *settings.Store
	eqs         eq.Settings
	userPresets map[string]preset
	saveEq      *core.Timer // settings are written once a slider drag pauses, not on every step
}

func NewEngine(loop *core.Loop, store *settings.Store, opts Options, ev EngineEvents) *Engine {
	e := &Engine{ev: ev, store: store, userPresets: map[string]preset{}}
	e.b = NewBackend(loop, opts, Events{
		PositionChanged: func(f int64) { call1(ev.PositionChanged, e.toMs(f)) },
		DurationChanged: func(f int64) { call1(ev.DurationChanged, e.toMs(f)) },
		StateChanged: func(s State) {
			if ev.StateChanged != nil {
				ev.StateChanged(s)
			}
		},
		EndOfStream:         func() { call0(ev.TrackFinished) },
		Error:               func(m string) { callS(ev.Error, m) },
		Notice:              func(m string) { callS(ev.Notice, m) },
		DevicesChanged:      func() { call0(ev.DevicesChanged) },
		OutputFormatChanged: func() { call0(ev.OutputFormatChanged) },
	})
	e.saveEq = loop.NewTimer(500*time.Millisecond, false, e.saveEqualizer)
	// Applied before anything is loaded, so the first track already opens the remembered device and mode.
	v := store.Get()
	e.b.SetOutputDevice(v.OutputDevice)
	e.b.SetExclusiveMode(v.Exclusive)
	e.restoreEqualizer(v)
	return e
}

func call0(f func()) {
	if f != nil {
		f()
	}
}

func call1(f func(int64), v int64) {
	if f != nil {
		f(v)
	}
}

func callS(f func(string), s string) {
	if f != nil {
		f(s)
	}
}

// Close writes pending settings and releases the audio device.
func (e *Engine) Close() {
	if e.saveEq.Active() {
		e.saveEqualizer() // a change made just before quitting
	}
	e.saveEq.Stop()
	e.b.Close()
}

func (e *Engine) toMs(frames int64) int64 {
	if r := e.b.SampleRate(); r > 0 {
		return frames * 1000 / int64(r)
	}
	return 0
}

// Load opens path if it is a supported audio file.
func (e *Engine) Load(path string) {
	if !formats.IsAudio(path) {
		callS(e.ev.Error, "Unsupported format: "+filepath.Base(path))
		return
	}
	e.b.Load(path)
}

func (e *Engine) Play()                         { e.b.Play() }
func (e *Engine) Pause()                        { e.b.Pause() }
func (e *Engine) Stop()                         { e.b.Stop() }
func (e *Engine) Unload()                       { e.b.Unload() }
func (e *Engine) SetVolume(linear float32)      { e.b.SetGain(linear) }
func (e *Engine) State() State                  { return e.b.State() }
func (e *Engine) Position() int64               { return e.toMs(e.b.PositionFrames()) }
func (e *Engine) Duration() int64               { return e.toMs(e.b.DurationFrames()) }
func (e *Engine) ClockMs() float64              { return e.b.ClockSeconds() * 1000 }
func (e *Engine) OutputDevices() []OutputDevice { return e.b.OutputDevices() }
func (e *Engine) OutputDeviceID() string        { return e.b.OutputDeviceID() }
func (e *Engine) ExclusiveMode() bool           { return e.b.ExclusiveMode() }
func (e *Engine) OutputFormat() OutputFormat    { return e.b.OutputFormat() }
func (e *Engine) VolumeLocked() bool            { return e.b.VolumeLocked() }
func (e *Engine) Equalizer() eq.Settings        { return e.eqs }

func (e *Engine) SeekMs(ms int64) { e.b.SeekTo(ms * int64(e.b.SampleRate()) / 1000) }

// SetOutputDevice selects and remembers the output device (by its stable id; empty = system default).
func (e *Engine) SetOutputDevice(id string) {
	e.store.Update(func(v *settings.Values) { v.OutputDevice = id })
	e.b.SetOutputDevice(id)
	call0(e.ev.DevicesChanged) // the selection is part of what the device list shows
}

func (e *Engine) SetExclusiveMode(on bool) {
	e.store.Update(func(v *settings.Values) { v.Exclusive = on })
	e.b.SetExclusiveMode(on)
}

// ---- equalizer ----

func (e *Engine) restoreEqualizer(v settings.Values) {
	e.eqs.Enabled = v.EqEnabled
	e.eqs.PreampDb = v.EqPreampDb
	for b := 0; b < eq.Bands && b < len(v.EqGainsDb); b++ {
		e.eqs.GainsDb[b] = v.EqGainsDb[b]
	}
	e.eqs = e.eqs.Clamped()
	for name, p := range v.EqPresets {
		if len(p.GainsDb) != eq.Bands {
			continue
		}
		var up preset
		up.preampDb = p.PreampDb
		copy(up.gainsDb[:], p.GainsDb)
		e.userPresets[name] = up
	}
	e.b.SetEqualizer(e.eqs)
}

func (e *Engine) saveEqualizer() {
	e.saveEq.Stop()
	s := e.eqs
	e.store.Update(func(v *settings.Values) {
		v.EqEnabled = s.Enabled
		v.EqPreampDb = s.PreampDb
		v.EqGainsDb = append([]float64(nil), s.GainsDb[:]...)
	})
}

// SetEqualizer applies (clamped) settings, and saves them once changes pause.
func (e *Engine) SetEqualizer(s eq.Settings) {
	c := s.Clamped()
	if c == e.eqs {
		return
	}
	e.eqs = c
	e.b.SetEqualizer(c)
	e.saveEq.Start()
	call0(e.ev.EqualizerChanged)
}

// EqPresetNames lists the built-in presets first, then the user's (sorted).
func (e *Engine) EqPresetNames() []string {
	var names []string
	for _, p := range builtinPresets {
		names = append(names, p.name)
	}
	var user []string
	for name := range e.userPresets {
		if !slices.Contains(names, name) {
			user = append(user, name)
		}
	}
	sort.Strings(user)
	return append(names, user...)
}

func (e *Engine) IsBuiltinEqPreset(name string) bool {
	for _, p := range builtinPresets {
		if p.name == name {
			return true
		}
	}
	return false
}

func (e *Engine) preset(name string) (preset, bool) {
	for _, p := range builtinPresets {
		if p.name == name {
			return p.preset, true
		}
	}
	p, ok := e.userPresets[name]
	return p, ok
}

// MatchingEqPreset is the preset the current gains equal, or "".
func (e *Engine) MatchingEqPreset() string {
	for _, name := range e.EqPresetNames() {
		if p, ok := e.preset(name); ok && p.preampDb == e.eqs.PreampDb && p.gainsDb == e.eqs.GainsDb {
			return name
		}
	}
	return ""
}

func (e *Engine) ApplyEqPreset(name string) {
	p, ok := e.preset(name)
	if !ok {
		return
	}
	s := e.eqs
	s.Enabled = true
	s.PreampDb = p.preampDb
	s.GainsDb = p.gainsDb
	e.SetEqualizer(s)
}

func (e *Engine) SaveEqPreset(rawName string) {
	name := strings.TrimSpace(rawName)
	if name == "" || e.IsBuiltinEqPreset(name) {
		return
	}
	p := preset{e.eqs.PreampDb, e.eqs.GainsDb}
	e.userPresets[name] = p
	e.store.Update(func(v *settings.Values) {
		if v.EqPresets == nil {
			v.EqPresets = map[string]settings.EqPreset{}
		}
		v.EqPresets[name] = settings.EqPreset{PreampDb: p.preampDb, GainsDb: append([]float64(nil), p.gainsDb[:]...)}
	})
	call0(e.ev.EqPresetsChanged)
	call0(e.ev.EqualizerChanged) // the current settings now match a preset
}

func (e *Engine) DeleteEqPreset(name string) {
	if _, ok := e.userPresets[name]; !ok {
		return
	}
	delete(e.userPresets, name)
	e.store.Update(func(v *settings.Values) { delete(v.EqPresets, name) })
	call0(e.ev.EqPresetsChanged)
	call0(e.ev.EqualizerChanged)
}
