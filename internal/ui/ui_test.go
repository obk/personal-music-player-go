package ui

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/gpu/headless"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"

	"musicplayer/internal/audio"
	"musicplayer/internal/core"
	"musicplayer/internal/library"
	"musicplayer/internal/player"
	"musicplayer/internal/settings"
)

// harness drives the UI headlessly: frames are laid out into ops, and input goes through Gio's router.
type harness struct {
	t    *testing.T
	loop *core.Loop
	p    *player.Player
	u    *UI
	r    input.Router
	ops  op.Ops
	now  time.Time
	size image.Point
	dir  string // removed after the player is closed
}

func newHarness(t *testing.T) *harness {
	h := &harness{t: t, loop: core.NewLoop(nil), now: time.Now(), size: image.Pt(1280, 800), dir: t.TempDir()}
	go h.loop.Run()
	store := settings.Open("")
	h.loop.Do(func() { h.p = player.New(h.loop, store, "", audio.Options{NullDevice: true}) })
	h.u = New(nil, h.loop, h.p, store, Host{})
	t.Cleanup(func() {
		h.loop.Do(h.p.Close)
		h.loop.Stop()
	})
	return h
}

func (h *harness) frame() {
	h.ops.Reset()
	gtx := layout.Context{Ops: &h.ops, Now: h.now, Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Constraints: layout.Exact(h.size), Source: h.r.Source()}
	h.loop.Lock()
	h.u.frame(gtx)
	h.loop.Unlock()
	h.r.Frame(&h.ops)
	h.now = h.now.Add(16 * time.Millisecond)
}

// frames runs n frames, pausing in between so posted work (scan results, timers) can run.
func (h *harness) frames(n int) {
	for i := 0; i < n; i++ {
		h.frame()
		time.Sleep(3 * time.Millisecond)
	}
}

func (h *harness) pointer(kind pointer.Kind, pt image.Point, buttons pointer.Buttons, mods key.Modifiers) {
	h.r.Queue(pointer.Event{Kind: kind, Source: pointer.Mouse, Buttons: buttons, Modifiers: mods,
		Position: f32.Pt(float32(pt.X), float32(pt.Y)), Time: time.Duration(h.now.UnixNano())})
}

func (h *harness) move(pt image.Point) {
	h.pointer(pointer.Move, pt, 0, 0)
	h.frames(3)
}

func (h *harness) clickWith(pt image.Point, b pointer.Buttons, mods key.Modifiers) {
	h.move(pt)
	h.pointer(pointer.Press, pt, b, mods)
	h.frame()
	h.pointer(pointer.Release, pt, 0, mods)
	h.frames(3)
}

func (h *harness) click(pt image.Point)       { h.clickWith(pt, pointer.ButtonPrimary, 0) }
func (h *harness) rightClick(pt image.Point)  { h.clickWith(pt, pointer.ButtonSecondary, 0) }
func (h *harness) doubleClick(pt image.Point) { h.click(pt); h.click(pt) }

func (h *harness) drag(from, to image.Point) {
	h.move(from)
	h.pointer(pointer.Press, from, pointer.ButtonPrimary, 0)
	h.frame()
	for i := 1; i <= 12; i++ {
		h.pointer(pointer.Move, from.Add(to.Sub(from).Mul(i).Div(12)), pointer.ButtonPrimary, 0)
		h.frame()
	}
	h.pointer(pointer.Release, to, 0, 0)
	h.frames(3)
}

func (h *harness) key(name key.Name, mods key.Modifiers) {
	h.r.Queue(key.Event{Name: name, Modifiers: mods, State: key.Press})
	h.frames(2)
}

func (h *harness) do(f func()) { h.loop.Do(f) }

func (h *harness) wait(what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		ok := false
		h.do(func() { ok = cond() })
		if ok {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("timed out waiting for %s", what)
		}
		h.frames(1)
	}
}

func writeWav(t *testing.T, path string, seconds float64) string {
	frames := int(48000 * seconds)
	var b bytes.Buffer
	le := func(v any) { binary.Write(&b, binary.LittleEndian, v) }
	b.WriteString("RIFF")
	le(uint32(36 + frames*4))
	b.WriteString("WAVEfmt ")
	le(uint32(16))
	le(uint16(1))
	le(uint16(2))
	le(uint32(48000))
	le(uint32(48000 * 4))
	le(uint16(4))
	le(uint16(16))
	b.WriteString("data")
	le(uint32(frames * 4))
	b.Write(make([]byte, frames*4))
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, b.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// loadLibrary adds a folder of WAV files and waits for the scan.
func (h *harness) loadLibrary(names ...string) string {
	dir := h.dir
	for _, n := range names {
		writeWav(h.t, filepath.Join(dir, n+".wav"), 3)
	}
	h.do(func() { h.p.AddToLibrary([]string{dir}) })
	h.wait("the scan", func() bool { return h.p.Library().Count() == len(names) && !h.p.Library().Scanning() })
	h.frames(2)
	return dir
}

// Rows of the Songs table at 1280x800, 1 px per dp: header 72, column header 32, rows 44.
func songRow(i int) image.Point { return image.Pt(420, 72+32+22+44*i) }

func TestPlaySelectAndMenus(t *testing.T) {
	h := newHarness(t)
	h.loadLibrary("a", "b", "c", "d")
	h.frames(2)

	// Double-click plays with the list as the context.
	h.doubleClick(songRow(1))
	h.wait("playback", func() bool { return h.p.Playing() && h.p.Current() != nil && h.p.Current().Info.Title == "b" })
	h.do(func() {
		if got := strings.Join(h.p.Queue().ContextRest(), ","); !strings.HasSuffix(got, "c.wav,") && got == "" {
			t.Errorf("context rest %q", got)
		}
	})

	// Ctrl-click extends the selection; the menu acts on it: Add to Queue.
	h.click(songRow(2))
	h.clickWith(songRow(3), pointer.ButtonPrimary, key.ModShortcut)
	h.rightClick(songRow(3))
	if !h.u.menu.open {
		t.Fatal("no context menu")
	}
	if h.u.menu.header != "2 tracks" {
		t.Errorf("menu header %q", h.u.menu.header)
	}
	// "Add to Queue" is the second item: 28 header + 4 padding + 32 + 16.
	h.click(h.u.menu.rect.Min.Add(image.Pt(60, 4+28+32+16)))
	h.do(func() {
		if n := len(h.p.Queue().UpNext()); n != 2 {
			t.Errorf("up next has %d tracks", n)
		}
	})

	// Enter plays the selection anchor; Space pauses.
	h.key(key.NameSpace, 0)
	h.wait("pause", func() bool { return !h.p.Playing() })

	// Delete asks before removing two tracks from the library.
	h.key(key.NameDeleteForward, 0)
	if !h.u.dialog.open {
		t.Fatal("no confirmation for removing 2 tracks")
	}
	h.key(key.NameEscape, 0)
	if h.u.dialog.open {
		t.Error("Escape did not close the dialog")
	}
}

func TestPlaylistDragAndQueue(t *testing.T) {
	h := newHarness(t)
	h.loadLibrary("a", "b", "c")
	h.do(func() { h.p.Library().NewPlaylist("Mix", nil) })
	h.frames(3)
	// The sidebar lists Liked Songs then Mix; drag row 0 onto Mix (y: playlists start below the nav).
	var mixY int
	h.do(func() {
		// nav: 16 + 36 + 16 = 68, five rows of 38, +16, header 28 + 4, Liked 38 → Mix at +18.
		mixY = 68 + 5*38 + 16 + 32 + 38 + 18
	})
	h.drag(songRow(0), image.Pt(120, mixY))
	h.do(func() {
		pl := h.p.Library().Playlist("Mix")
		if pl == nil || len(pl.IDs) != 1 {
			t.Fatalf("playlist after drop: %+v", pl)
		}
	})

	// The queue drawer opens from the bar and accepts dropped tracks into Up Next.
	h.do(func() { h.u.setQueueOpen(true) })
	h.frames(3)
	h.drag(songRow(2), image.Pt(1280-160, 72+60))
	h.do(func() {
		if n := len(h.p.Queue().UpNext()); n != 1 {
			t.Errorf("up next after dropping on the drawer: %d", n)
		}
	})
}

func TestViewsRender(t *testing.T) {
	h := newHarness(t)
	h.loadLibrary("Artist A - x", "b", "c")
	for _, r := range []route{{kind: routeSongs}, {kind: routeAlbums}, {kind: routeArtists}, {kind: routeFolders},
		{kind: routeLiked}, {kind: routeNowPlaying}} {
		h.u.route = r
		for _, size := range []image.Point{{1280, 800}, {960, 600}, {1920, 1080}} {
			h.size = size
			h.frames(3)
		}
	}
	h.size = image.Pt(1280, 800)
	h.do(func() { h.u.route = route{kind: routeAlbum, key: h.p.Library().Albums()[0].Key} })
	h.frames(3)
	h.do(func() { h.u.search.editor.SetText("b") })
	h.frames(3)
	if h.u.route.kind != routeSearch {
		t.Errorf("typing a search did not open the results (%v)", h.u.route)
	}
}

// TestScreenshots renders views to PNG files in $MP_SHOTS (skipped otherwise), for visual review.
func TestScreenshots(t *testing.T) {
	dir := os.Getenv("MP_SHOTS")
	lib := os.Getenv("MP_SHOTS_LIBRARY")
	if dir == "" || lib == "" {
		t.Skip("set MP_SHOTS and MP_SHOTS_LIBRARY")
	}
	h := newHarness(t)
	h.do(func() { h.p.AddToLibrary([]string{lib}) })
	h.wait("the scan", func() bool { return h.p.Library().Count() > 0 && !h.p.Library().Scanning() })
	h.frames(5)
	w, err := headless.NewWindow(h.size.X, h.size.Y)
	if err != nil {
		t.Skip(err)
	}
	defer w.Release()
	shot := func(name string) {
		time.Sleep(150 * time.Millisecond) // covers load
		h.frames(20)
		h.frame()
		if err := w.Frame(&h.ops); err != nil {
			t.Fatal(err)
		}
		img := image.NewRGBA(image.Rectangle{Max: h.size})
		if err := w.Screenshot(img); err != nil {
			t.Fatal(err)
		}
		f, _ := os.Create(filepath.Join(dir, name+".png"))
		png.Encode(f, img)
		f.Close()
	}
	shot("songs")
	h.doubleClick(songRow(2))
	h.move(songRow(4))
	shot("songs-playing-hover")
	h.rightClick(songRow(4))
	h.move(h.u.menu.rect.Min.Add(image.Pt(60, 4+32+32+16)))
	shot("menu")
	h.key(key.NameEscape, 0)
	h.u.route = route{kind: routeAlbums}
	h.move(image.Pt(340, 200))
	shot("albums")
	h.do(func() { h.u.route = route{kind: routeAlbum, key: h.p.Library().Albums()[0].Key} })
	shot("album")
	h.u.route = route{kind: routeArtists}
	shot("artists")
	h.do(func() {
		h.p.AddToQueue(library.IDs(h.p.Library().Songs()[:2]))
		h.u.setQueueOpen(true)
	})
	h.u.route = route{kind: routeSongs}
	shot("queue")
	h.do(func() { h.u.setQueueOpen(false) })
	h.u.route = route{kind: routeNowPlaying}
	shot("nowplaying")
	h.u.store.Update(func(v *settings.Values) { v.Theme = "light" })
	h.u.route = route{kind: routeSongs}
	shot("songs-light")
	h.u.route = route{kind: routeAlbums}
	shot("albums-light")
	h.u.store.Update(func(v *settings.Values) { v.Theme = "dark" })
	h.u.route = route{kind: routeSongs}
	h.u.eqMenu.open = true
	shot("eq")
	h.u.eqMenu.open, h.u.outMenu.open = false, true
	shot("output")
	h.u.outMenu.open = false
	h.do(func() { h.u.openTagEditor(h.p.Library().Songs()[0]) })
	shot("tageditor")
	h.key(key.NameEscape, 0)
	h.u.OSDragOver(600, 300, true)
	shot("drop")
}
