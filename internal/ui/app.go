package ui

import (
	"fmt"
	"image"
	"image/color"
	"sync"
	"time"

	"gioui.org/app"
	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/widget"

	"musicplayer/internal/core"
	"musicplayer/internal/library"
	"musicplayer/internal/player"
	"musicplayer/internal/settings"
)

// Host provides what the UI needs from the platform. Every field may be nil.
type Host struct {
	AddFolder      func()              // pick a folder and add it to the library
	AddFiles       func()              // pick files and add them
	ExportPlaylist func(name string)   // pick a file and export the playlist to it
	ShowInExplorer func(path string)   // reveal a file
	SystemLight    func() bool         // the OS prefers the light theme
	Event          func(e event.Event) // sees every window event first
}

type routeKind int

const (
	routeNowPlaying routeKind = iota
	routeSongs
	routeAlbums
	routeAlbum
	routeArtists
	routeArtist
	routeFolders
	routeFolder
	routePlaylist
	routeLiked
	routeSearch
)

type route struct {
	kind routeKind
	key  string
}

// ref is the queue context reference of a route ("album:<key>"), used to mark where music plays from.
func (r route) ref() string {
	switch r.kind {
	case routeSongs:
		return "songs"
	case routeLiked:
		return "liked"
	case routeSearch:
		return "search"
	}
	return [...]string{"nowplaying", "songs", "albums", "album", "artists", "artist", "folders", "folder",
		"playlist", "liked", "search"}[r.kind] + ":" + r.key
}

type UI struct {
	win    *app.Window
	loop   *core.Loop
	p      *player.Player
	lib    *library.Library
	store  *settings.Store
	host   Host
	shaper *text.Shaper
	images *imageCache

	maximized bool
	title     string
	size      image.Point

	route   route
	history []route

	positionMs float64 // display position this frame

	search    searchState
	sidebar   sidebarState
	tables    map[string]*tableState
	grid      gridState
	lists     map[string]*listState
	header    headerState
	queueOpen bool
	drawer    drawerState
	bar       barState
	np        nowPlayingState
	lyr       lyricsViewState
	eqMenu    eqMenuState
	outMenu   outMenuState
	menu      menuState
	dialog    dialogState
	toast     toastState
	tooltip   tooltipState
	dnd       dragState
	osDrop    osDropState
	winCtl    [3]iconButton
	keys      int
	after     []func()

	textFocus bool // a text field has the keyboard: shortcuts are off

	org   image.Point // current drawing origin in window coordinates (see push)
	mouse mouseTracker
}

// later runs f once the current frame has been laid out (for changes that would disturb a list mid-layout).
func (u *UI) later(f func()) { u.after = append(u.after, f) }

func New(w *app.Window, loop *core.Loop, p *player.Player, store *settings.Store, host Host) *UI {
	u := &UI{win: w, loop: loop, p: p, lib: p.Library(), store: store, host: host, shaper: newShaper(),
		images: newImageCache(), tables: map[string]*tableState{}, lists: map[string]*listState{},
		route: route{kind: routeSongs}}
	u.queueOpen = store.Get().QueueOpen
	p.OnToast = func(t player.Toast) { u.toast.show(t) }
	if w != nil { // nil in tests, which drive frames themselves
		p.OnChange = w.Invalidate
		loop.SetOnChange(w.Invalidate)
	}
	return u
}

// Invalidate asks for a new frame (safe from any goroutine).
func (u *UI) Invalidate() {
	if u.win != nil {
		u.win.Invalidate()
	}
}

// Run processes window events until the window is closed.
func (u *UI) Run() error {
	var ops op.Ops
	for {
		e := u.win.Event()
		if u.host.Event != nil {
			u.host.Event(e)
		}
		switch e := e.(type) {
		case app.DestroyEvent:
			return e.Err
		case app.ConfigEvent:
			u.maximized = e.Config.Mode == app.Maximized
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			u.loop.Lock()
			u.frame(gtx)
			u.loop.Unlock()
			e.Frame(gtx.Ops)
		}
	}
}

// ---- navigation ----

func (u *UI) navigate(r route) {
	if r == u.route {
		return
	}
	u.history = append(u.history, u.route)
	if len(u.history) > 50 {
		u.history = u.history[1:]
	}
	u.route = r
	u.menu.open = false
}

func (u *UI) goBack() {
	if len(u.history) == 0 {
		return
	}
	u.route = u.history[len(u.history)-1]
	u.history = u.history[:len(u.history)-1]
}

// ---- theme ----

func (u *UI) themeSetting() string { return u.store.Get().Theme }

func (u *UI) cycleTheme() {
	next := map[string]string{"": "light", "light": "dark", "dark": ""}[u.themeSetting()]
	u.store.Update(func(v *settings.Values) { v.Theme = next })
}

func (u *UI) applyTheme() {
	light := false
	switch u.themeSetting() {
	case "light":
		light = true
	case "dark":
	default:
		light = u.host.SystemLight != nil && u.host.SystemLight()
	}
	if light {
		pal = &lightPalette
	} else {
		pal = &darkPalette
	}
}

// ---- geometry ----

type geometry struct {
	ws      image.Point
	sidebar image.Rectangle
	main    image.Rectangle
	drawer  image.Rectangle // empty when closed
	overlay bool            // the drawer floats above the main view
	bar     image.Rectangle
	rail    bool
}

func (u *UI) geometry(gtx layout.Context) geometry {
	ws := gtx.Constraints.Max
	g := geometry{ws: ws, rail: ws.X < dp(gtx, 1100)}
	sw := dp(gtx, 240)
	if g.rail {
		sw = dp(gtx, 64)
	}
	barH := dp(gtx, 88)
	g.bar = image.Rect(0, ws.Y-barH, ws.X, ws.Y)
	g.sidebar = image.Rect(0, 0, sw, g.bar.Min.Y)
	g.main = image.Rect(sw, 0, ws.X, g.bar.Min.Y)
	if u.queueOpen {
		dw := dp(gtx, 320)
		g.drawer = image.Rect(ws.X-dw, 0, ws.X, g.bar.Min.Y)
		if ws.X >= dp(gtx, 1440) {
			g.main.Max.X -= dw
		} else {
			g.overlay = true
		}
	}
	return g
}

// ---- frame ----

func (u *UI) frame(gtx layout.Context) {
	beginFrame(gtx.Now)
	frameMetric = gtx.Metric.PxPerDp
	u.org = image.Point{}
	u.trackMouse(gtx)
	u.applyTheme()
	u.images.sweep()
	u.size = gtx.Constraints.Max
	u.updateTitle()
	u.dnd.beginFrame()

	// Clicked buttons take the keyboard focus; hand it back to the window unless a text field holds it.
	u.textFocus = gtx.Focused(&u.search.editor) || u.dialog.hasTextFocus(gtx) || (u.eqMenu.naming && gtx.Focused(&u.eqMenu.editor))
	if !u.textFocus && !u.dialog.open {
		gtx.Execute(key.FocusCmd{})
	}
	u.handleKeys(gtx)

	u.positionMs = u.p.UpdateClock()
	if u.p.Playing() {
		gtx.Execute(op.InvalidateCmd{})
	}

	g := u.geometry(gtx)
	paint.Fill(gtx.Ops, pal.bg)
	event.Op(gtx.Ops, &u.keys)

	u.layoutMain(gtx, g)
	u.layoutSidebar(gtx, g)
	if !g.drawer.Empty() {
		u.layoutDrawer(gtx, g)
	}
	u.layoutBar(gtx, g)
	u.layoutWindowControls(gtx, g)
	u.layoutToast(gtx, g)
	u.layoutPopups(gtx, g)
	u.layoutDragGhost(gtx)
	u.layoutOSDrop(gtx, g)
	u.layoutMenu(gtx)
	u.layoutDialog(gtx, g)
	u.layoutTooltip(gtx)
	u.registerMouse(gtx)

	for len(u.after) > 0 {
		f := u.after[0]
		u.after = u.after[1:]
		f()
		gtx.Execute(op.InvalidateCmd{})
	}
	endFrame(gtx)
}

func (u *UI) updateTitle() {
	t := "Music Player"
	if tr := u.p.Current(); tr != nil {
		t = tr.Info.Title + " – Music Player"
	}
	if t != u.title {
		u.title = t
		if u.win != nil {
			u.win.Option(app.Title(t))
		}
	}
}

func (u *UI) perform(a system.Action) {
	if u.win != nil {
		u.win.Perform(a)
	}
}

// layoutWindowControls draws minimize / maximize / close in the window's top-right corner (the window is
// frameless; the main view's header doubles as the title bar).
func (u *UI) layoutWindowControls(gtx layout.Context, g geometry) {
	w, h := dp(gtx, 46), dp(gtx, 32)
	x := g.ws.X - 3*w
	glyphs := []string{icMinimize, icMaximize, icClose}
	if u.maximized {
		glyphs[1] = icRestore
	}
	for i := range u.winCtl {
		func() {
			defer op.Offset(image.Pt(x+i*w, 0)).Push(gtx.Ops).Pop()
			st := iconBtn(glyphs[i], 16)
			st.size = 32
			st.color = pal.text2
			if i == 2 {
				st.hoverBg, st.hoverCol = pal.danger, argb(0xFFFFFFFF)
			}
			gg := gtx
			gg.Constraints = layout.Exact(image.Pt(w, h))
			// Wider than tall: centre a 32 dp button in each 46 dp slot.
			defer op.Offset(image.Pt((w-dp(gtx, 32))/2, 0)).Push(gtx.Ops).Pop()
			if _, clicked := u.winCtl[i].layout(u, gg, st); clicked {
				switch i {
				case 0:
					u.perform(system.ActionMinimize)
				case 1:
					if u.maximized {
						u.perform(system.ActionUnmaximize)
					} else {
						u.perform(system.ActionMaximize)
					}
				case 2:
					u.perform(system.ActionClose)
				}
			}
		}()
	}
}

// moveArea makes r drag the window (and maximize on double click), like a title bar.
func moveArea(gtx layout.Context, r image.Rectangle) {
	if r.Empty() {
		return
	}
	defer clip.Rect(r).Push(gtx.Ops).Pop()
	system.ActionInputOp(system.ActionMove).Add(gtx.Ops)
}

// ---- keyboard ----

func (u *UI) handleKeys(gtx layout.Context) {
	filters := []event.Filter{key.Filter{Name: key.NameEscape}}
	if !u.textFocus && !u.dialog.open {
		filters = append(filters,
			key.Filter{Name: key.NameSpace},
			key.Filter{Name: "F", Required: key.ModShortcut},
			key.Filter{Name: "Z", Required: key.ModShortcut},
			key.Filter{Name: "A", Required: key.ModShortcut},
			key.Filter{Name: "Q", Required: key.ModShortcut},
			key.Filter{Name: "I", Required: key.ModShortcut},
			key.Filter{Name: "E", Required: key.ModShortcut | key.ModShift},
			key.Filter{Name: key.NameUpArrow, Optional: key.ModShift},
			key.Filter{Name: key.NameDownArrow, Optional: key.ModShift},
			key.Filter{Name: key.NameReturn, Optional: key.ModShortcut | key.ModShift},
			key.Filter{Name: key.NameEnter, Optional: key.ModShortcut | key.ModShift},
			key.Filter{Name: key.NameDeleteForward},
			key.Filter{Name: key.NameLeftArrow, Required: key.ModShift},
			key.Filter{Name: key.NameRightArrow, Required: key.ModShift},
			key.Filter{Name: key.NameLeftArrow, Required: key.ModShortcut},
			key.Filter{Name: key.NameRightArrow, Required: key.ModShortcut},
			key.Filter{Name: key.NameBack},
		)
	}
	for {
		ev, ok := gtx.Event(filters...)
		if !ok {
			return
		}
		e, ok := ev.(key.Event)
		if !ok || e.State != key.Press {
			continue
		}
		t := u.activeTable()
		switch e.Name {
		case key.NameEscape:
			switch {
			case u.dialog.open:
				u.dialog.close()
			case u.menu.open:
				u.menu.open = false
			case u.eqMenu.open || u.outMenu.open:
				u.eqMenu.open, u.outMenu.open = false, false
			case u.search.query() != "":
				u.search.clear(u)
			case u.queueOpen:
				u.setQueueOpen(false)
			}
		case key.NameSpace:
			u.p.TogglePlayPause()
		case "F":
			gtx.Execute(key.FocusCmd{Tag: &u.search.editor})
		case "Z":
			u.p.Undo()
		case key.NameLeftArrow, key.NameRightArrow:
			switch {
			case e.Modifiers.Contain(key.ModShift):
				u.p.SeekBy(map[bool]int64{true: -10000, false: 10000}[e.Name == key.NameLeftArrow])
			case e.Name == key.NameLeftArrow:
				u.p.Previous()
			default:
				u.p.Next()
			}
		case key.NameBack:
			u.goBack()
		default:
			if t != nil {
				u.tableKey(t, e)
			}
		}
	}
}

func (u *UI) setQueueOpen(open bool) {
	u.queueOpen = open
	u.store.Update(func(v *settings.Values) { v.QueueOpen = open })
}

// ---- toast ----

// toastState is a transient message with an optional action ("Undo"), shown for 5 s above the playback bar.
type toastState struct {
	t       player.Toast
	pending bool
	shownAt time.Time
	visible fade
	action  widget.Clickable
}

func (t *toastState) show(msg player.Toast) {
	t.t = msg
	t.pending = true
}

func (u *UI) layoutToast(gtx layout.Context, g geometry) {
	t := &u.toast
	if t.pending {
		t.pending = false
		t.shownAt = gtx.Now
	}
	hold := 3500 * time.Millisecond
	if t.t.Action != "" {
		hold = 5 * time.Second
	}
	shown := t.t.Text != "" && gtx.Now.Sub(t.shownAt) < hold
	if shown {
		gtx.Execute(op.InvalidateCmd{At: t.shownAt.Add(hold)})
	}
	a := t.visible.to(boolf(shown), normal)
	if a <= 0.01 {
		return
	}
	if t.action.Clicked(gtx) && t.t.Do != nil {
		do := t.t.Do
		t.t = player.Toast{}
		u.later(do)
	}
	lc, ld := u.text(gtx, t.t.Text, textStyle{size: fsCell, weight: font.Medium, color: pal.text1})
	pad := dp(gtx, 16)
	h := dp(gtx, 44)
	w := ld.Size.X + 2*pad
	var ac op.CallOp
	var ad layout.Dimensions
	if t.t.Action != "" {
		ac, ad = u.text(gtx, t.t.Action, textStyle{size: fsCell, weight: font.Bold, color: pal.accent})
		w += ad.Size.X + dp(gtx, 24)
	}
	cx := g.main.Min.X + g.main.Dx()/2
	r := image.Rect(cx-w/2, g.bar.Min.Y-dp(gtx, 16)-h, cx+w/2, g.bar.Min.Y-dp(gtx, 16))
	r = r.Add(image.Pt(0, int((1-a)*float32(dp(gtx, 8)))))
	defer paintOpacity(gtx, a).Pop()
	shadow(gtx.Ops, r, dp(gtx, rPanel), dp(gtx, 24), dp(gtx, 8))
	fillRRect(gtx.Ops, r, dp(gtx, rPanel), pal.elevated)
	if pal.light {
		strokeRRect(gtx.Ops, r, dp(gtx, rPanel), 1, pal.divider)
	}
	place(gtx.Ops, image.Pt(r.Min.X+pad, r.Min.Y+(h-ld.Size.Y)/2), lc)
	if t.t.Action != "" {
		bx := r.Max.X - pad - ad.Size.X - dp(gtx, 8)
		at(gtx, image.Pt(bx, r.Min.Y+dp(gtx, 6)), image.Pt(ad.Size.X+dp(gtx, 16), h-dp(gtx, 12)), func(gtx layout.Context) layout.Dimensions {
			sz := image.Pt(ad.Size.X+dp(gtx, 16), h-dp(gtx, 12))
			gtx.Constraints = layout.Exact(sz)
			return t.action.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				if t.action.Hovered() {
					fillRRect(gtx.Ops, image.Rectangle{Max: sz}, dp(gtx, rSmall), pal.hover)
				}
				place(gtx.Ops, image.Pt(dp(gtx, 8), (sz.Y-ad.Size.Y)/2), ac)
				pointerCursor(gtx, image.Rectangle{Max: sz}, pointer.CursorPointer)
				return layout.Dimensions{Size: sz}
			})
		})
	}
}

// ---- files dropped from Explorer ----

// osDropState is written by the window thread (drag feedback) and read by the frame.
type osDropState struct {
	mu     sync.Mutex
	active bool
	pos    image.Point
}

// OSDragOver is called by the platform while files are dragged over the window.
func (u *UI) OSDragOver(x, y int, active bool) {
	u.osDrop.mu.Lock()
	u.osDrop.active, u.osDrop.pos = active, image.Pt(x, y)
	u.osDrop.mu.Unlock()
	u.Invalidate()
}

// OSDrop is called by the platform with files dropped on the window at (x, y).
func (u *UI) OSDrop(paths []string, x, y int) {
	u.loop.Post(func() {
		ids := u.p.AddToLibrary(paths)
		if u.drawer.rect.Min.X > 0 && image.Pt(x, y).In(u.drawer.rect) && len(ids) > 0 {
			u.p.Queue().InsertUpNext(u.drawer.dropIndexAt(y), ids)
		}
	})
}

func (u *UI) layoutOSDrop(gtx layout.Context, g geometry) {
	u.osDrop.mu.Lock()
	active, pos := u.osDrop.active, u.osDrop.pos
	u.osDrop.mu.Unlock()
	if !active {
		return
	}
	full := image.Rectangle{Max: g.ws}
	fillRect(gtx.Ops, full, withAlpha(pal.accent, 0.08))
	inset := full.Inset(dp(gtx, 12))
	dashedRect(gtx, inset, dp(gtx, rPanel), pal.accent)
	label := "Drop to add to library"
	if !g.drawer.Empty() && pos.In(g.drawer) {
		label = "Drop to add to library and queue"
		dashedRect(gtx, g.drawer.Inset(dp(gtx, 6)), dp(gtx, rPanel), pal.accent)
	}
	c, d := u.text(gtx, label, textStyle{size: fsSection, weight: font.SemiBold, color: pal.text1})
	pill := image.Rect(0, 0, d.Size.X+dp(gtx, 32), d.Size.Y+dp(gtx, 20))
	pill = pill.Add(image.Pt((g.ws.X-pill.Dx())/2, (g.ws.Y-pill.Dy())/2))
	fillRRect(gtx.Ops, pill, dp(gtx, rPanel), pal.elevated)
	place(gtx.Ops, pill.Min.Add(image.Pt(dp(gtx, 16), dp(gtx, 10))), c)
}

// dashedRect draws a 2 dp dashed outline (straight edges dashed, corners left open).
func dashedRect(gtx layout.Context, r image.Rectangle, radius int, c color.NRGBA) {
	t, dash, gap := dp(gtx, 2), dp(gtx, 8), dp(gtx, 6)
	for x := r.Min.X + radius; x < r.Max.X-radius; x += dash + gap {
		e := min(x+dash, r.Max.X-radius)
		fillRect(gtx.Ops, image.Rect(x, r.Min.Y, e, r.Min.Y+t), c)
		fillRect(gtx.Ops, image.Rect(x, r.Max.Y-t, e, r.Max.Y), c)
	}
	for y := r.Min.Y + radius; y < r.Max.Y-radius; y += dash + gap {
		e := min(y+dash, r.Max.Y-radius)
		fillRect(gtx.Ops, image.Rect(r.Min.X, y, r.Min.X+t, e), c)
		fillRect(gtx.Ops, image.Rect(r.Max.X-t, y, r.Max.X, e), c)
	}
}

// ---- helpers ----

// countLabel is "1 track" / "3 tracks".
func countLabel(n int, noun string) string { return player.Count(n, noun) }

// durationLabel is "3 h 12 m" / "42 min".
func durationLabel(ms int64) string {
	m := ms / 60000
	if m >= 60 {
		return itoa(m/60) + " h " + itoa(m%60) + " m"
	}
	return itoa(m) + " min"
}

func itoa(v int64) string { return formatInt(v) }

func formatInt(v int64) string {
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
		if v == 0 {
			break
		}
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// groupDigits formats 1204 as "1,204".
func groupDigits(n int) string {
	s := formatInt(int64(n))
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func toastf(format string, args ...any) player.Toast {
	return player.Toast{Text: fmt.Sprintf(format, args...)}
}
