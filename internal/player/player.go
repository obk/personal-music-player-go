// Package player is the facade the UI and the OS integrations talk to. It wires the audio engine, the library, the
// queue, the metadata service and the lyrics together, and exposes state and actions. All methods must be called
// on the loop (with its lock held).
package player

import (
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"musicplayer/internal/audio"
	"musicplayer/internal/core"
	"musicplayer/internal/eq"
	"musicplayer/internal/formats"
	"musicplayer/internal/library"
	"musicplayer/internal/lyrics"
	"musicplayer/internal/metadata"
	"musicplayer/internal/queue"
	"musicplayer/internal/settings"
)

// Observer receives notifications on the loop. Any field may be nil.
type Observer struct {
	StateChanged    func()
	PositionChanged func(ms int64)
	DurationChanged func()
	TrackChanged    func() // a new current track, or its tags / cover arrived
	CurrentCleared  func()
	PlaylistChanged func() // the library or the queue changed
	NowPlaying      func(np metadata.NowPlaying)
}

// Toast is a transient message, optionally with an action ("Undo").
type Toast struct {
	Text   string
	Action string
	Do     func()
}

type Player struct {
	loop   *core.Loop
	store  *settings.Store
	engine *audio.Engine
	meta   *metadata.Service
	covers *metadata.CoverCache
	lib    *library.Library
	q      *queue.Queue
	lyr    lyricsState

	observers []Observer
	// OnToast shows a transient message. Set by the UI.
	OnToast func(t Toast)
	// OnChange asks the UI to redraw. Set by the UI.
	OnChange func()

	volume            float64
	coverSmall        *image.RGBA
	coverMedium       *image.RGBA
	coverLarge        *image.RGBA
	coverRevision     int
	trackSerial       int
	nowPlayingRequest uint64 // the metadata request whose result is still wanted
	heldPosMs         float64
	resumeAt          int64 // after (re)loading the current track, seek here (ms; -1 = no)
	resumePaused      bool
	editing           map[string]bool // tracks whose tags are being written
	saveVolume        *core.Timer
}

// New builds the player. libraryFile is where the library is kept ("" = memory only); opts selects the audio
// device backend (tests use the null device).
func New(loop *core.Loop, store *settings.Store, libraryFile string, opts audio.Options) *Player {
	p := &Player{loop: loop, store: store, volume: 0.8, covers: metadata.NewCoverCache(96 << 20), resumeAt: -1,
		editing: map[string]bool{}}
	p.meta = metadata.NewService(loop, p.covers, store, metadata.Events{
		TagsReady:       func(b []metadata.TagResult) { p.lib.OnTagsReady(b) },
		NowPlayingReady: p.onNowPlaying,
		PlaylistParsed:  p.onPlaylistParsed,
		CoverLoaded:     p.changed,
	})
	p.lib = library.New(loop, p.meta, libraryFile)
	p.lib.OnChange = func() {
		p.notify(func(o Observer) {
			call(o.PlaylistChanged)
			call(o.TrackChanged) // the current track's tags may have arrived
		})
	}
	p.lib.OnRemoved = func(ids []string) { p.q.Remove(ids) }
	p.q = queue.New(queue.Events{
		CurrentChanged: p.onCurrentChanged,
		CurrentCleared: p.onCurrentCleared,
		Changed:        func() { p.notify(func(o Observer) { call(o.PlaylistChanged) }) },
	})
	p.engine = audio.NewEngine(loop, store, opts, audio.EngineEvents{
		PositionChanged: func(ms int64) {
			// While playing, the UI's frame clock drives the lyrics through UpdateClock; engine ticks only apply
			// when it is not (a seek while paused, a track change), so the two never disagree.
			if !p.Playing() {
				p.heldPosMs = p.engine.ClockMs()
				p.lyr.onPosition(ms)
			}
			p.notify(func(o Observer) {
				if o.PositionChanged != nil {
					o.PositionChanged(ms)
				}
			})
		},
		DurationChanged: func(int64) { p.notify(func(o Observer) { call(o.DurationChanged) }) },
		StateChanged: func(audio.State) {
			p.heldPosMs = p.engine.ClockMs()
			p.notify(func(o Observer) { call(o.StateChanged) })
		},
		TrackFinished:       func() { p.q.OnTrackFinished() },
		Error:               p.message,
		Notice:              p.message,
		DevicesChanged:      p.changed,
		OutputFormatChanged: p.changed,
		EqualizerChanged:    p.changed,
		EqPresetsChanged:    p.changed,
	})
	if v := store.Get().Volume; v != nil {
		p.volume = math.Max(0, math.Min(1, *v))
	}
	p.engine.SetVolume(float32(p.volume))
	p.saveVolume = loop.NewTimer(500*time.Millisecond, false, func() {
		v := p.volume
		p.store.Update(func(s *settings.Values) { s.Volume = &v })
	})
	p.lib.Load()
	return p
}

// Close saves the library, releases the audio device and stops the workers.
func (p *Player) Close() {
	if p.saveVolume.Active() {
		v := p.volume
		p.store.Update(func(s *settings.Values) { s.Volume = &v })
	}
	p.lib.Save()
	p.engine.Close()
	p.meta.Close()
}

func call(f func()) {
	if f != nil {
		f()
	}
}

func (p *Player) Observe(o Observer) { p.observers = append(p.observers, o) }

func (p *Player) notify(f func(Observer)) {
	for _, o := range p.observers {
		f(o)
	}
	p.changed()
}

func (p *Player) changed() {
	if p.OnChange != nil {
		p.OnChange()
	}
}

func (p *Player) toast(t Toast) {
	if p.OnToast != nil {
		p.OnToast(t)
	}
	p.changed()
}

func (p *Player) message(text string) { p.toast(Toast{Text: text}) }

// ---- wiring ----

// onCurrentChanged: a track change does no file I/O here. Thumbnails the scan already cached are shown at once,
// and the large cover and the lyrics are read on a worker (onNowPlaying).
func (p *Player) onCurrentChanged(id string) {
	t := p.lib.Track(id)
	if t == nil {
		return
	}
	p.coverSmall = p.covers.Find(t.Path, metadata.Small)
	p.coverMedium = p.covers.Find(t.Path, metadata.Medium)
	p.coverLarge = nil
	p.coverRevision++
	p.trackSerial++
	p.lyr.clear()
	p.lyr.offsetMs = 0 // the track's saved offset arrives with its lyrics
	p.nowPlayingRequest = p.meta.LoadNowPlaying(t.Path)

	p.engine.Load(t.Path)
	if p.resumeAt >= 0 {
		p.engine.SeekMs(p.resumeAt)
		p.resumeAt = -1
	}
	if p.resumePaused {
		p.resumePaused = false
	} else {
		p.engine.Play()
	}
	p.notify(func(o Observer) { call(o.TrackChanged) })
}

func (p *Player) onCurrentCleared() {
	p.engine.Unload()
	p.nowPlayingRequest = 0 // a result still in flight is for a track that is gone
	p.lyr.clear()
	p.coverSmall, p.coverMedium, p.coverLarge = nil, nil, nil
	p.coverRevision++
	p.notify(func(o Observer) {
		call(o.CurrentCleared)
		call(o.TrackChanged)
	})
}

func (p *Player) onNowPlaying(np metadata.NowPlaying) {
	if np.Request != p.nowPlayingRequest {
		return // the user moved on to another track meanwhile
	}
	p.coverSmall, p.coverMedium, p.coverLarge = np.Small, np.Medium, np.Large
	p.coverRevision++
	p.lyr.offsetMs = np.LyricsOffsetMs
	p.lyr.setLines(np.Lyrics)
	p.notify(func(o Observer) {
		if o.NowPlaying != nil {
			o.NowPlaying(np)
		}
		call(o.TrackChanged)
	})
}

// onPlaylistParsed imports an .m3u/.m3u8 as a playlist named after the file.
func (p *Player) onPlaylistParsed(path string, paths []string, ok bool) {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	switch {
	case !ok:
		p.message("Could not read " + filepath.Base(path))
	case len(paths) == 0:
		p.message(filepath.Base(path) + " has no playable tracks")
	default:
		ids := p.lib.AddPaths(paths)
		name = p.lib.NewPlaylist(name, ids)
		p.message(fmt.Sprintf("Imported playlist “%s” (%s)", name, Count(len(ids), "track")))
	}
}

// Count formats "1 track" / "3 tracks".
func Count(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// ---- library and queue ----

func (p *Player) Library() *library.Library { return p.lib }
func (p *Player) Queue() *queue.Queue       { return p.q }

// AddToLibrary adds files and folders (watched from now on) and imports playlists. It returns the IDs of the audio
// files given directly, in order. Unsupported files are reported.
func (p *Player) AddToLibrary(paths []string) []string {
	var media []string
	skipped := 0
	for _, path := range paths {
		fi, err := os.Stat(path)
		switch {
		case err != nil:
			skipped++
		case fi.IsDir(), formats.IsAudio(path):
			media = append(media, path)
		case formats.IsPlaylist(path):
			p.meta.ParsePlaylist(path)
		default:
			skipped++
		}
	}
	ids := p.lib.AddPaths(media)
	if skipped > 0 {
		p.message(Count(skipped, "file") + " skipped (unsupported format)")
	}
	return ids
}

// OpenAndPlay adds paths to the library and plays the audio files among them ("Open with", command line).
func (p *Player) OpenAndPlay(paths []string) {
	ids := p.AddToLibrary(paths)
	if len(ids) > 0 {
		p.PlayContext("Opened files", "files", ids, 0)
	}
}

// PlayContext plays ids[start] with the list as the context. Up Next is kept; replacing a different list can be
// undone from the toast.
func (p *Player) PlayContext(name, ref string, ids []string, start int) {
	pos := p.engine.Position()
	replaced := p.q.PlayContext(queue.Context{Name: name, Ref: ref, IDs: ids}, start, pos)
	switch {
	case replaced:
		p.toast(Toast{Text: "Queue replaced", Action: "Undo", Do: p.Undo})
	case len(p.q.UpNext()) > 0:
		p.message("Playing from " + name + " · Up Next kept")
	}
}

// Undo restores the queue replaced by the last PlayContext, resuming its track where it was.
func (p *Player) Undo() {
	wasPlaying := p.Playing()
	cur := p.q.Current()
	if id, pos, ok := p.q.Undo(); ok && id != "" && id != cur {
		// The queue already reloaded it from the start; go back to where it was.
		p.engine.SeekMs(pos)
		if !wasPlaying {
			p.engine.Pause()
		}
	}
}

func (p *Player) PlayNext(ids []string) {
	p.q.PlayNext(ids)
	p.message("Playing next: " + p.describe(ids))
}

func (p *Player) AddToQueue(ids []string) {
	p.q.AddToQueue(ids)
	p.message("Added to queue: " + p.describe(ids))
}

func (p *Player) describe(ids []string) string {
	if len(ids) == 1 {
		if t := p.lib.Track(ids[0]); t != nil {
			return t.Info.Title
		}
	}
	return Count(len(ids), "track")
}

// RemoveFromLibrary takes tracks out of the library (the files stay on disk).
func (p *Player) RemoveFromLibrary(ids []string) {
	p.lib.Remove(ids)
	p.message("Removed " + Count(len(ids), "track") + " from the library")
}

func (p *Player) ToggleLike(id string) { p.lib.ToggleLike(id) }

// ExportPlaylist writes a playlist as an extended M3U.
func (p *Player) ExportPlaylist(name, path string) {
	pl := p.lib.Playlist(name)
	if pl == nil {
		return
	}
	if filepath.Ext(path) == "" {
		path += ".m3u8"
	}
	var b strings.Builder
	b.WriteString("#EXTM3U\n")
	for _, t := range p.lib.Resolve(pl.IDs) {
		fmt.Fprintf(&b, "#EXTINF:%d,%s\n%s\n", int(math.Round(float64(t.Info.DurationMs)/1000)), t.DisplayName(), t.Path)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		p.message("Could not write " + path)
	} else {
		p.message("Playlist exported")
	}
}

// EditTags writes new tags into a track's file (without re-encoding) and rereads it. A playing track is released
// for the rewrite and resumed where it was.
func (p *Player) EditTags(id string, fields metadata.TagFields) {
	t := p.lib.Track(id)
	if t == nil || p.editing[id] {
		return
	}
	p.editing[id] = true
	isCurrent := id == p.q.Current()
	wasPlaying := p.Playing()
	pos := p.engine.Position()
	if isCurrent {
		p.engine.Unload()
	}
	path := t.Path
	go func() {
		err := metadata.WriteTags(path, fields)
		p.loop.Post(func() {
			delete(p.editing, id)
			if err != nil {
				p.message(err.Error())
			} else {
				p.lib.Refresh(id)
			}
			if isCurrent && p.q.Current() == id {
				p.resumeAt, p.resumePaused = pos, !wasPlaying
				p.onCurrentChanged(id)
			}
		})
	}()
}

// RequestCover loads a track's thumbnails for views that show many covers.
func (p *Player) RequestCover(path string) { p.meta.RequestCover(path) }

// ---- transport ----

func (p *Player) Play() {
	switch {
	case p.engine.State() == audio.Paused:
		p.engine.Play()
	case p.q.Current() == "":
		if !p.q.Next() {
			if songs := p.lib.Songs(); len(songs) > 0 {
				p.PlayContext("Songs", "songs", library.IDs(songs), 0)
			}
		}
	default:
		p.engine.Play()
	}
}

func (p *Player) Pause() { p.engine.Pause() }
func (p *Player) Stop()  { p.engine.Stop() }

func (p *Player) TogglePlayPause() {
	if p.engine.State() == audio.Playing {
		p.Pause()
	} else {
		p.Play()
	}
}

func (p *Player) Next() { p.q.Next() }

// Previous restarts the track first, like most players; it goes back only near the track's start.
func (p *Player) Previous() {
	if p.engine.Position() > 3000 || !p.q.Previous() {
		p.engine.SeekMs(0)
	}
}

func (p *Player) SeekMs(ms int64)      { p.engine.SeekMs(max(0, min(ms, p.engine.Duration()))) }
func (p *Player) SeekBy(deltaMs int64) { p.SeekMs(p.engine.Position() + deltaMs) }
func (p *Player) ToggleShuffle()       { p.q.SetShuffle(!p.q.Shuffle()) }
func (p *Player) CycleRepeat()         { p.q.CycleRepeat() }

// ---- state ----

func (p *Player) Playing() bool                { return p.engine.State() == audio.Playing }
func (p *Player) State() audio.State           { return p.engine.State() }
func (p *Player) Position() int64              { return p.engine.Position() }
func (p *Player) Duration() int64              { return p.engine.Duration() }
func (p *Player) Volume() float64              { return p.volume }
func (p *Player) Shuffle() bool                { return p.q.Shuffle() }
func (p *Player) RepeatMode() queue.RepeatMode { return p.q.RepeatMode() }
func (p *Player) HasTrack() bool               { return p.Current() != nil }
func (p *Player) TrackCount() int              { return p.lib.Count() }
func (p *Player) Covers() *metadata.CoverCache { return p.covers }
func (p *Player) TrackSerial() int             { return p.trackSerial }
func (p *Player) CoverRevision() int           { return p.coverRevision }

// Current returns the current track, or nil.
func (p *Player) Current() *library.Track { return p.lib.Track(p.q.Current()) }

// Cover returns the best available image of the current track for a use: "small", "medium" or "large". Each
// falls back to the neighbouring size until the right one arrives.
func (p *Player) Cover(size string) *image.RGBA {
	switch size {
	case "small":
		return firstNonNil(p.coverSmall, p.coverMedium)
	case "medium":
		return firstNonNil(p.coverMedium, p.coverLarge)
	}
	return firstNonNil(p.coverLarge, p.coverMedium)
}

func firstNonNil(imgs ...*image.RGBA) *image.RGBA {
	for _, i := range imgs {
		if i != nil {
			return i
		}
	}
	return nil
}

func (p *Player) SetVolume(v float64) {
	v = math.Max(0, math.Min(1, v))
	if v == p.volume {
		return
	}
	p.volume = v
	p.engine.SetVolume(float32(v))
	p.saveVolume.Start()
	p.changed()
}

// ---- playback clock ----

// UpdateClock returns the sample-accurate playback position in ms while playing, and drives the lyric selector
// from it, so the seek bar and the lyrics follow the same time source. The UI calls it once per frame.
func (p *Player) UpdateClock() float64 {
	if !p.Playing() {
		return p.heldPosMs
	}
	ms := p.engine.ClockMs()
	p.lyr.onPosition(int64(ms))
	return ms
}

// ---- lyrics ----

func (p *Player) HasLyrics() bool       { return len(p.lyr.lines) > 0 }
func (p *Player) Lyrics() []lyrics.Line { return p.lyr.lines }
func (p *Player) ActiveLyric() int      { return p.lyr.active }
func (p *Player) ActiveWord() int       { return p.lyr.word }
func (p *Player) LyricsOffsetMs() int   { return p.lyr.offsetMs }

// SetLyricsOffsetMs sets the per-track timing correction (positive shows lyrics earlier), remembered per file.
func (p *Player) SetLyricsOffsetMs(ms int) {
	ms = max(-30000, min(30000, ms))
	t := p.Current()
	if t == nil || ms == p.lyr.offsetMs {
		return
	}
	p.lyr.offsetMs = ms
	p.lyr.onPosition(p.lyr.lastPos)
	key := metadata.LyricsOffsetKey(t.Path)
	p.store.Update(func(v *settings.Values) {
		if ms == 0 {
			delete(v.LyricsOffsets, key)
			return
		}
		if v.LyricsOffsets == nil {
			v.LyricsOffsets = map[string]int{}
		}
		v.LyricsOffsets[key] = ms
	})
	p.changed()
}

// ---- output ----

type DeviceEntry struct {
	ID, Name, Detail string
	IsDefault        bool
}

// OutputDevices lists "System default" (id "", with the default device's name as detail) and then every device.
func (p *Player) OutputDevices() []DeviceEntry {
	devs := p.engine.OutputDevices()
	defaultName := ""
	for _, d := range devs {
		if d.IsDefault {
			defaultName = d.Name
		}
	}
	out := []DeviceEntry{{ID: "", Name: "System default", Detail: defaultName}}
	for _, d := range devs {
		out = append(out, DeviceEntry{ID: d.ID, Name: d.Name, IsDefault: d.IsDefault})
	}
	return out
}

func (p *Player) OutputDeviceID() string           { return p.engine.OutputDeviceID() }
func (p *Player) SetOutputDevice(id string)        { p.engine.SetOutputDevice(id) }
func (p *Player) ExclusiveMode() bool              { return p.engine.ExclusiveMode() }
func (p *Player) SetExclusiveMode(on bool)         { p.engine.SetExclusiveMode(on); p.changed() }
func (p *Player) VolumeLocked() bool               { return p.engine.VolumeLocked() }
func (p *Player) OutputFormat() audio.OutputFormat { return p.engine.OutputFormat() }

// OutputSummary is e.g. "96 kHz · 24-bit · Exclusive", or "" when nothing is open.
func (p *Player) OutputSummary() string {
	f := p.engine.OutputFormat()
	if f.SampleRate <= 0 {
		return ""
	}
	// Bit-perfect 24-bit audio travels in a 32-bit container: show what the samples carry.
	depth := "32-bit float"
	if !f.IsFloat {
		bits := f.Bits
		if f.BitPerfect && f.SourceBits > 0 {
			bits = f.SourceBits
		}
		depth = strconv.Itoa(bits) + "-bit"
	}
	mode := "Shared"
	if f.Exclusive {
		mode = "Exclusive"
	}
	return KHz(f.SampleRate) + " · " + depth + " · " + mode
}

// KHz formats a sample rate: 44100 -> "44.1 kHz", 48000 -> "48 kHz".
func KHz(rate int) string {
	if rate%1000 != 0 {
		return strconv.FormatFloat(float64(rate)/1000, 'f', 1, 64) + " kHz"
	}
	return strconv.Itoa(rate/1000) + " kHz"
}

// ---- equalizer ----

func (p *Player) Equalizer() eq.Settings { return p.engine.Equalizer() }

func (p *Player) SetEqEnabled(on bool) {
	s := p.engine.Equalizer()
	s.Enabled = on
	p.engine.SetEqualizer(s)
}

func (p *Player) SetEqPreamp(db float64) {
	s := p.engine.Equalizer()
	s.PreampDb = db
	p.engine.SetEqualizer(s)
}

func (p *Player) SetEqGain(band int, db float64) {
	if band < 0 || band >= eq.Bands {
		return
	}
	s := p.engine.Equalizer()
	s.GainsDb[band] = db
	p.engine.SetEqualizer(s)
}

func (p *Player) ResetEq() {
	s := p.engine.Equalizer()
	s.PreampDb = 0
	s.GainsDb = [eq.Bands]float64{}
	p.engine.SetEqualizer(s)
}

func (p *Player) EqPresets() []string             { return p.engine.EqPresetNames() }
func (p *Player) EqPreset() string                { return p.engine.MatchingEqPreset() }
func (p *Player) ApplyEqPreset(name string)       { p.engine.ApplyEqPreset(name) }
func (p *Player) SaveEqPreset(name string)        { p.engine.SaveEqPreset(name) }
func (p *Player) DeleteEqPreset(name string)      { p.engine.DeleteEqPreset(name) }
func (p *Player) IsBuiltinEqPreset(n string) bool { return p.engine.IsBuiltinEqPreset(n) }

// EqFrequencies are the band labels: "31", "62", ... "16k".
func EqFrequencies() []string {
	out := make([]string, eq.Bands)
	for i, f := range eq.Frequencies {
		if f >= 1000 {
			out[i] = strconv.Itoa(int(f/1000)) + "k"
		} else {
			out[i] = strconv.Itoa(int(f))
		}
	}
	return out
}
