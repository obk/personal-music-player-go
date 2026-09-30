//go:build windows

package win

import (
	"bytes"
	"image"
	"image/png"
	"runtime"
	"time"
	"unsafe"

	"musicplayer/internal/audio"
	"musicplayer/internal/core"
	"musicplayer/internal/metadata"
	"musicplayer/internal/player"
)

var (
	iidSMTCInterop   = guid("{ddb0472d-c911-4a1f-86d9-dc3d71a95f5a}")
	iidSMTC          = guid("{99fa3ff4-1742-42a6-902e-087d41f965ec}")
	iidSMTC2         = guid("{ea98d2f6-7f3c-4af2-a586-72889808efb1}")
	iidMusicProps2   = guid("{00368462-97d3-44b9-b00f-008afcefaf18}")
	iidTimelineProps = guid("{5125316a-c3a2-475b-8507-93534dc88f15}")
	iidRASRefStatics = guid("{857309dc-3fbf-4e7d-986f-ef3b1a07a964}")
	iidRandomAccess  = guid("{905a0fe1-bc53-11df-8c49-001e4fc686da}")
	iidButtonHandler = guid("{0557e996-7b23-5bae-aa81-ea0d671143a4}")
	iidSeekHandler   = guid("{44e34f15-bdc0-50a7-ace4-39e91fb753f1}")
)

// Vtable indices (IInspectable occupies 0-5).
const (
	smtcPutPlaybackStatus  = 7
	smtcGetDisplayUpdater  = 8
	smtcPutIsEnabled       = 11
	smtcPutIsPlayEnabled   = 13
	smtcPutIsStopEnabled   = 15
	smtcPutIsPauseEnabled  = 17
	smtcPutIsPrevEnabled   = 25
	smtcPutIsNextEnabled   = 27
	smtcAddButtonPressed   = 32
	smtc2UpdateTimeline    = 12
	smtc2AddPositionChange = 13
	duPutType              = 7
	duPutThumbnail         = 11
	duGetMusicProperties   = 12
	duClearAll             = 16
	duUpdate               = 17
	musicPutTitle          = 7
	musicPutArtist         = 11
	music2PutAlbumTitle    = 7
	music2PutTrackNumber   = 9
	tlPutStartTime         = 7
	tlPutEndTime           = 9
	tlPutMinSeekTime       = 11
	tlPutMaxSeekTime       = 13
	tlPutPosition          = 15
	rasCreateFromStream    = 8
	argsGet                = 6 // Button / RequestedPlaybackPosition
)

// MediaPlaybackStatus and SystemMediaTransportControlsButton values.
const (
	statusClosed  = 0
	statusStopped = 2
	statusPlaying = 3
	statusPaused  = 4

	buttonPlay     = 0
	buttonPause    = 1
	buttonStop     = 2
	buttonNext     = 6
	buttonPrevious = 7
)

type trackMeta struct {
	path, title, artist, album string
	track                      int
	thumb                      *image.RGBA
}

// SMTC connects the player to Windows' SystemMediaTransportControls for the main window. Media keys reach us as
// SMTC button presses routed by the shell to the active media session, so nothing is registered globally and
// other players keep their keys; the same session feeds the volume / media overlay and the lock screen.
//
// All WinRT calls run on one dedicated OS thread; the player is only touched on the loop.
type SMTC struct {
	loop *core.Loop
	p    *player.Player
	cmds chan func()

	smtc, smtc2 unsafe.Pointer // SMTC thread only

	// loop side
	timelineTick *core.Timer
	reportedMs   int64
	reportedAt   time.Time
	thumb        *image.RGBA
	thumbFor     string
	sent         trackMeta
	sentStatus   [6]int
	attached     bool
}

// NewSMTC attaches media controls to the window hwnd. It returns at once (without taking the loop's lock: the
// WinRT calls may need the window's thread); the controls come alive on the loop once they are set up, and stay
// inert if Windows does not provide them.
func NewSMTC(loop *core.Loop, p *player.Player, hwnd uintptr) *SMTC {
	s := &SMTC{loop: loop, p: p, cmds: make(chan func(), 64), sentStatus: [6]int{-1}}
	ready := make(chan bool, 1)
	go s.thread(hwnd, ready)
	go func() {
		if <-ready {
			loop.Post(s.attach)
		}
	}()
	return s
}

// attach runs on the loop.
func (s *SMTC) attach() {
	p := s.p
	s.attached = true
	s.timelineTick = s.loop.NewTimer(time.Second, true, s.updateTimeline)
	p.Observe(player.Observer{
		StateChanged:    func() { s.updateStatus(); s.updateTimeline() },
		DurationChanged: s.updateTimeline,
		PositionChanged: func(ms int64) {
			// The overlay does not extrapolate: refresh on seeks (a jump from where we said it would be), and once
			// a second while playing (timelineTick).
			expected := s.reportedMs
			if p.Playing() {
				expected += time.Since(s.reportedAt).Milliseconds()
			}
			if d := ms - expected; d > 1500 || d < -1500 {
				s.updateTimeline()
			}
		},
		TrackChanged:    func() { s.updateMetadata(); s.updateStatus() },
		CurrentCleared:  func() { s.updateMetadata(); s.updateStatus() },
		PlaylistChanged: s.updateStatus,
		// The cover arrives with the now-playing extraction when the scan had not cached it yet.
		NowPlaying: func(np metadata.NowPlaying) {
			if t := p.Current(); t != nil && t.Path == np.Path && s.thumbFor != np.Path && np.Medium != nil {
				s.thumb, s.thumbFor = np.Medium, np.Path
				s.updateMetadata()
			}
		},
	})
	s.updateStatus()
	s.updateMetadata()
}

func (s *SMTC) run(f func()) {
	select {
	case s.cmds <- f:
	default: // the SMTC thread is stuck; drop rather than block the loop
	}
}

func (s *SMTC) thread(hwnd uintptr, ready chan<- bool) {
	runtime.LockOSThread()
	procRoInitialize.Call(1) // multithreaded; events arrive on thread-pool threads anyway
	interop := activationFactory("Windows.Media.SystemMediaTransportControls", &iidSMTCInterop)
	if interop == nil {
		ready <- false
		return
	}
	var smtc unsafe.Pointer
	hr := vcall(interop, 6, hwnd, uintptr(unsafe.Pointer(&iidSMTC)), uintptr(unsafe.Pointer(&smtc)))
	release(interop)
	if failed(hr) || smtc == nil {
		ready <- false
		return
	}
	s.smtc = smtc
	s.smtc2 = queryInterface(smtc, &iidSMTC2)

	buttons := newDelegate(iidButtonHandler, func(_, args unsafe.Pointer) {
		var b int32
		if failed(vcall(args, argsGet, uintptr(unsafe.Pointer(&b)))) {
			return
		}
		s.loop.Post(func() { s.onButton(int(b)) })
	})
	var token int64
	vcall(smtc, smtcAddButtonPressed, uintptr(buttons.obj), uintptr(unsafe.Pointer(&token)))
	if s.smtc2 != nil {
		seek := newDelegate(iidSeekHandler, func(_, args unsafe.Pointer) {
			var ts int64 // 100 ns units
			if failed(vcall(args, argsGet, uintptr(unsafe.Pointer(&ts)))) {
				return
			}
			s.loop.Post(func() { s.p.SeekMs(ts / 10000) })
		})
		vcall(s.smtc2, smtc2AddPositionChange, uintptr(seek.obj), uintptr(unsafe.Pointer(&token)))
	}
	vcall(smtc, smtcPutIsEnabled, 1)
	ready <- true
	for f := range s.cmds {
		f()
	}
}

func (s *SMTC) onButton(b int) {
	switch b {
	case buttonPlay:
		s.p.Play()
	case buttonPause:
		s.p.Pause()
	case buttonStop:
		s.p.Stop()
	case buttonNext:
		s.p.Next()
	case buttonPrevious:
		s.p.Previous()
	}
}

func boolArg(b bool) uintptr {
	if b {
		return 1
	}
	return 0
}

func (s *SMTC) updateStatus() {
	hasTracks := s.p.TrackCount() > 0
	hasCurrent := s.p.HasTrack()
	status := statusClosed
	if hasCurrent {
		switch s.p.State() {
		case audio.Playing:
			status = statusPlaying
		case audio.Paused:
			status = statusPaused
		default:
			status = statusStopped
		}
	}
	if status == statusPlaying {
		if !s.timelineTick.Active() {
			s.timelineTick.Start()
		}
	} else {
		s.timelineTick.Stop()
	}
	b := func(v bool) int {
		if v {
			return 1
		}
		return 0
	}
	next := [6]int{status, b(hasTracks), b(hasCurrent), b(hasCurrent), b(hasTracks), b(hasTracks)}
	if next == s.sentStatus {
		return
	}
	s.sentStatus = next
	s.run(func() {
		// The play/pause key is delivered as Play or Pause depending on this status, so keep it exact.
		vcall(s.smtc, smtcPutPlaybackStatus, uintptr(status))
		vcall(s.smtc, smtcPutIsPlayEnabled, boolArg(hasTracks))
		vcall(s.smtc, smtcPutIsPauseEnabled, boolArg(hasCurrent))
		vcall(s.smtc, smtcPutIsStopEnabled, boolArg(hasCurrent))
		vcall(s.smtc, smtcPutIsNextEnabled, boolArg(hasTracks))
		vcall(s.smtc, smtcPutIsPrevEnabled, boolArg(hasTracks))
	})
}

func (s *SMTC) updateMetadata() {
	t := s.p.Current()
	if t == nil {
		if s.sent == (trackMeta{}) {
			return
		}
		s.sent = trackMeta{}
		s.run(func() {
			var du unsafe.Pointer
			if !failed(vcall(s.smtc, smtcGetDisplayUpdater, uintptr(unsafe.Pointer(&du)))) {
				vcall(du, duClearAll)
				vcall(du, duUpdate)
				release(du)
			}
		})
		return
	}
	if s.thumbFor != t.Path { // the scan's cached thumbnail, if there is one yet
		s.thumb = s.p.Covers().Find(t.Path, metadata.Medium)
		if s.thumb != nil {
			s.thumbFor = t.Path
		} else {
			s.thumbFor = ""
		}
	}
	m := trackMeta{t.Path, t.Info.Title, t.Info.Artist, t.Info.Album, max(0, t.Info.TrackNumber), s.thumb}
	if m == s.sent {
		return
	}
	s.sent = m
	s.run(func() { s.sendMetadata(m) })
}

// sendMetadata runs on the SMTC thread.
func (s *SMTC) sendMetadata(m trackMeta) {
	var du unsafe.Pointer
	if failed(vcall(s.smtc, smtcGetDisplayUpdater, uintptr(unsafe.Pointer(&du)))) {
		return
	}
	defer release(du)
	vcall(du, duPutType, 1) // MediaPlaybackType.Music
	var music unsafe.Pointer
	if !failed(vcall(du, duGetMusicProperties, uintptr(unsafe.Pointer(&music)))) {
		for _, f := range []struct {
			idx int
			s   string
		}{{musicPutTitle, m.title}, {musicPutArtist, m.artist}} {
			h := newHString(f.s)
			vcall(music, f.idx, uintptr(h))
			h.free()
		}
		if music2 := queryInterface(music, &iidMusicProps2); music2 != nil {
			h := newHString(m.album)
			vcall(music2, music2PutAlbumTitle, uintptr(h))
			h.free()
			vcall(music2, music2PutTrackNumber, uintptr(m.track))
			release(music2)
		}
		release(music)
	}
	ref := thumbnailStream(m.thumb)
	vcall(du, duPutThumbnail, uintptr(ref))
	release(ref)
	vcall(du, duUpdate)
}

// thumbnailStream: PNG in memory -> IStream -> IRandomAccessStream -> RandomAccessStreamReference, all synchronous.
func thumbnailStream(img *image.RGBA) unsafe.Pointer {
	if img == nil {
		return nil
	}
	var buf bytes.Buffer
	if png.Encode(&buf, img) != nil || buf.Len() == 0 {
		return nil
	}
	data := buf.Bytes()
	mem, _, _ := procSHCreateMemStream.Call(uintptr(unsafe.Pointer(&data[0])), uintptr(len(data)))
	if mem == 0 {
		return nil
	}
	defer release(ptr(mem))
	var stream unsafe.Pointer
	r, _, _ := procCreateRandomAccessStreamOverStream.Call(mem, 0, uintptr(unsafe.Pointer(&iidRandomAccess)),
		uintptr(unsafe.Pointer(&stream)))
	if failed(r) {
		return nil
	}
	defer release(stream)
	statics := activationFactory("Windows.Storage.Streams.RandomAccessStreamReference", &iidRASRefStatics)
	if statics == nil {
		return nil
	}
	defer release(statics)
	var ref unsafe.Pointer
	if failed(vcall(statics, rasCreateFromStream, uintptr(stream), uintptr(unsafe.Pointer(&ref)))) {
		return nil
	}
	return ref
}

func (s *SMTC) updateTimeline() {
	duration := s.p.Duration()
	pos := max(0, min(s.p.Position(), duration))
	s.reportedMs, s.reportedAt = pos, time.Now()
	if s.smtc2 == nil {
		return
	}
	s.run(func() {
		inst := activateInstance("Windows.Media.SystemMediaTransportControlsTimelineProperties")
		if inst == nil {
			return
		}
		defer release(inst)
		tl := queryInterface(inst, &iidTimelineProps)
		if tl == nil {
			return
		}
		defer release(tl)
		ts := func(ms int64) uintptr { return uintptr(ms * 10000) } // TimeSpan: 100 ns units
		vcall(tl, tlPutStartTime, 0)
		vcall(tl, tlPutEndTime, ts(duration))
		vcall(tl, tlPutMinSeekTime, 0)
		vcall(tl, tlPutMaxSeekTime, ts(duration))
		vcall(tl, tlPutPosition, ts(pos))
		vcall(s.smtc2, smtc2UpdateTimeline, uintptr(tl))
	})
}

// Close clears the overlay and disables the controls.
func (s *SMTC) Close() {
	if s == nil || !s.attached {
		return
	}
	s.timelineTick.Stop()
	done := make(chan struct{})
	s.run(func() {
		var du unsafe.Pointer
		if !failed(vcall(s.smtc, smtcGetDisplayUpdater, uintptr(unsafe.Pointer(&du)))) {
			vcall(du, duClearAll)
			vcall(du, duUpdate)
			release(du)
		}
		vcall(s.smtc, smtcPutIsEnabled, 0)
		close(done)
	})
	select {
	case <-done:
	case <-time.After(time.Second):
	}
}
