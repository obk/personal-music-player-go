package ui

import (
	"image"
	"image/color"
	"math"
	"slices"
	"time"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/text"
	"gioui.org/widget"

	"musicplayer/internal/library"
)

// tableSpec describes one track list: what it shows and where playback started from it comes from.
type tableSpec struct {
	key       string
	tracks    []*library.Track
	ctxName   string
	ctxRef    string
	albumNums bool   // the # column shows the track number (album view)
	playlist  string // a user playlist: rows can be reordered and removed from it
}

type tableState struct {
	list        widget.List
	sel         map[string]bool
	anchor      int
	rows        map[string]*rowState
	lastSerial  int
	lastPos     layout.Position
	userAt      time.Time
	lastClickID string
	lastClickAt time.Time
	spec        tableSpec
}

type rowState struct {
	hovered  bool
	hoverPos f32.Point
	pressed  bool
	pressPos f32.Point
	deferSel bool
	more     iconButton
}

func (u *UI) table(key string) *tableState {
	t := u.tables[key]
	if t == nil {
		t = &tableState{sel: map[string]bool{}, rows: map[string]*rowState{}, anchor: -1}
		t.list.Axis = layout.Vertical
		u.tables[key] = t
	}
	return t
}

// activeTable is the table of the view on screen (for keyboard commands), or nil.
func (u *UI) activeTable() *tableState {
	if t := u.tables[u.route.ref()]; t != nil && t.spec.key == u.route.ref() {
		return t
	}
	return nil
}

// selectedIDs returns the selection in list order.
func (t *tableState) selectedIDs() []string {
	var out []string
	for _, tr := range t.spec.tracks {
		if t.sel[tr.ID] {
			out = append(out, tr.ID)
		}
	}
	return out
}

func (t *tableState) selectedIndexes() []int {
	var out []int
	for i, tr := range t.spec.tracks {
		if t.sel[tr.ID] {
			out = append(out, i)
		}
	}
	return out
}

func (t *tableState) selectOnly(i int) {
	clear(t.sel)
	if i >= 0 && i < len(t.spec.tracks) {
		t.sel[t.spec.tracks[i].ID] = true
	}
	t.anchor = i
}

func (t *tableState) selectRange(from, to int) {
	clear(t.sel)
	if from > to {
		from, to = to, from
	}
	for i := max(from, 0); i <= to && i < len(t.spec.tracks); i++ {
		t.sel[t.spec.tracks[i].ID] = true
	}
}

// columns computes the column layout for a table width.
type columns struct {
	pad, num, title, artist, album, dur, gap, more int
	showArtist, showAlbum                          bool
	rowH                                           int
}

func (u *UI) columns(gtx layout.Context, w int) columns {
	c := columns{pad: dp(gtx, 16), num: dp(gtx, 48), dur: dp(gtx, 64), gap: dp(gtx, 16), more: dp(gtx, 36)}
	c.showAlbum = w >= dp(gtx, 900)
	c.showArtist = w >= dp(gtx, 700)
	avail := w - 2*c.pad - c.more
	if c.showArtist {
		c.artist = int(float32(avail) * 0.22)
	}
	if c.showAlbum {
		c.album = int(float32(avail) * 0.22)
	}
	c.title = avail - c.num - c.dur - c.gap - c.artist - c.album
	if c.showArtist {
		c.title -= c.gap
	}
	if c.showAlbum {
		c.title -= c.gap
	}
	c.rowH = dp(gtx, 44)
	if !c.showArtist {
		c.rowH = dp(gtx, 56) // the artist moves under the title
	}
	return c
}

// layoutTable draws a track list filling r (in the current coordinates).
func (u *UI) layoutTable(gtx layout.Context, r image.Rectangle, spec tableSpec) {
	t := u.table(spec.key)
	t.spec = spec
	cols := u.columns(gtx, r.Dx())
	defer u.push(gtx, r.Min)()
	w, h := r.Dx(), r.Dy()

	// Sticky column header.
	hh := dp(gtx, 32)
	x := cols.pad
	head := func(label string, x, width int, align text.Alignment) {
		if width <= 0 {
			return
		}
		c, d := u.text(gtx, label, textStyle{size: fsColumn, weight: font.SemiBold, color: pal.text3})
		if align == text.End {
			x += width - d.Size.X
		} else if align == text.Middle {
			x += (width - d.Size.X) / 2
		}
		place(gtx.Ops, image.Pt(x, (hh-d.Size.Y)/2), c)
	}
	head("#", x, cols.num, text.Middle)
	x += cols.num
	head("TITLE", x, cols.title, text.Start)
	x += cols.title + cols.gap
	if cols.showArtist {
		head("ARTIST", x, cols.artist, text.Start)
		x += cols.artist + cols.gap
	}
	if cols.showAlbum {
		head("ALBUM", x, cols.album, text.Start)
		x += cols.album + cols.gap
	}
	head("TIME", x, cols.dur, text.End)
	fillRect(gtx.Ops, image.Rect(0, hh-1, w, hh), pal.divider)

	listR := image.Rect(0, hh, w, h)
	if len(spec.tracks) == 0 {
		c, d := u.text(gtx, "No tracks", textStyle{size: fsBody, color: pal.text3})
		place(gtx.Ops, image.Pt((w-d.Size.X)/2, hh+dp(gtx, 48)), c)
		return
	}

	// Follow the playing track when it changes: only if its row is off-screen and the list was not scrolled by
	// hand in the last 5 s.
	rowH := cols.rowH
	visible := max(1, listR.Dy()/rowH)
	if serial := u.p.TrackSerial(); serial != t.lastSerial {
		t.lastSerial = serial
		if cur := u.p.Queue().Current(); cur != "" && gtx.Now.Sub(t.userAt) > 5*time.Second {
			if i := slices.IndexFunc(spec.tracks, func(tr *library.Track) bool { return tr.ID == cur }); i >= 0 {
				first := t.list.Position.First
				if i < first || i >= first+visible {
					t.list.Position.First = max(0, i-visible/3)
					t.list.Position.Offset = 0
				}
			}
		}
	}

	func() {
		defer u.push(gtx, listR.Min)()
		defer clip.Rect(image.Rectangle{Max: listR.Size()}).Push(gtx.Ops).Pop()
		gg := gtx
		gg.Constraints = layout.Exact(listR.Size())
		listOrg := u.org
		t.list.Layout(gg, len(spec.tracks), func(gtx layout.Context, i int) layout.Dimensions {
			y := (i-t.list.Position.First)*rowH - t.list.Position.Offset
			u.org = listOrg.Add(image.Pt(0, y))
			defer func() { u.org = listOrg }()
			u.layoutRow(gtx, t, i, cols, w)
			return layout.Dimensions{Size: image.Pt(w, rowH)}
		})
		// Reordering (or dropping into) a playlist: an insertion line between rows.
		if spec.playlist != "" && u.dnd.active {
			win := u.wr(image.Rectangle{Max: listR.Size()})
			if u.dnd.pos.In(win) {
				my := u.dnd.pos.Y - win.Min.Y + t.list.Position.First*rowH + t.list.Position.Offset
				idx := max(0, min(len(spec.tracks), int(math.Round(float64(my)/float64(rowH)))))
				ly := (idx-t.list.Position.First)*rowH - t.list.Position.Offset
				fillRect(gtx.Ops, image.Rect(dp(gtx, 8), ly-dp(gtx, 1), w-dp(gtx, 8), ly+dp(gtx, 1)), pal.accent)
				u.dnd.target = dropTarget{kind: dropInList, playlist: spec.playlist, index: idx}
			}
		}
	}()
	if t.list.Position != t.lastPos {
		if t.lastPos.First != t.list.Position.First || t.lastPos.Offset != t.list.Position.Offset {
			t.userAt = gtx.Now
		}
		t.lastPos = t.list.Position
	}
}

// layoutRow draws row i and handles its input.
func (u *UI) layoutRow(gtx layout.Context, t *tableState, i int, cols columns, w int) {
	tr := t.spec.tracks[i]
	rs := t.rows[tr.ID]
	if rs == nil {
		rs = &rowState{}
		t.rows[tr.ID] = rs
	}
	rowH := cols.rowH
	u.rowInput(gtx, t, i, rs, w, rowH)

	current := tr.ID == u.p.Queue().Current()
	selected := t.sel[tr.ID]
	menuOpen := u.menu.open && rs.more.click.Pressed()
	bg := pal.bg
	switch {
	case selected:
		bg = pal.selected
	case rs.hovered && !u.dnd.active:
		bg = pal.hover
	}
	inset := dp(gtx, 8)
	fillRRect(gtx.Ops, image.Rect(inset, 0, w-inset, rowH), dp(gtx, rSmall), bgOrNone(bg))

	x := cols.pad
	// # column: the now-playing equaliser, a play icon on hover, else the number.
	switch {
	case current:
		bw := dp(gtx, 10)
		eqBars(gtx, image.Pt(x+(cols.num-bw)/2, (rowH-dp(gtx, 12))/2), u.p.Playing(), pal.accent)
	case rs.hovered && !u.dnd.active:
		c, d := u.icon(gtx, icPlay, 16, pal.text1)
		place(gtx.Ops, image.Pt(x+(cols.num-d.Size.X)/2, (rowH-d.Size.Y)/2), c)
	default:
		n := formatInt(int64(i + 1))
		if t.spec.albumNums {
			n = "—"
			if tr.Info.TrackNumber > 0 {
				n = formatInt(int64(tr.Info.TrackNumber))
			}
		}
		c, d := u.text(gtx, n, textStyle{size: fsCell, color: pal.text2})
		place(gtx.Ops, image.Pt(x+(cols.num-d.Size.X)/2, (rowH-d.Size.Y)/2), c)
	}
	x += cols.num

	hoverCell := func(x0, x1 int, text string, truncated bool) {
		if truncated && rs.hovered && rs.hoverPos.X >= float32(x0) && rs.hoverPos.X < float32(x1) {
			u.tipAtMouse(rs, text)
		}
	}
	if tr.Pending {
		// Tags not read yet: skeleton bars pulsing between 100% and 50% opacity over 1.2 s.
		a := float32(0.75 + 0.25*math.Sin(float64(gtx.Now.UnixNano())/1e9*2*math.Pi/1.2))
		clk.animating = true
		bar := func(x0, width int) {
			bh := dp(gtx, 10)
			fillRRect(gtx.Ops, image.Rect(x0, (rowH-bh)/2, x0+width, (rowH+bh)/2), dp(gtx, rSmall), withAlpha(skeleton(), a))
		}
		bar(x, cols.title*6/10)
		x += cols.title + cols.gap
		if cols.showArtist {
			bar(x, cols.artist*4/10)
			x += cols.artist + cols.gap
		}
		if cols.showAlbum {
			bar(x, cols.album*4/10)
		}
	} else {
		titleCol := pal.text1
		if current {
			titleCol = pal.accent
		}
		artist, artistCol := tr.Info.Artist, pal.text2
		if artist == "" {
			artist, artistCol = "Unknown Artist", pal.text3
		}
		album, albumCol := tr.Info.Album, pal.text2
		if album == "" {
			album, albumCol = "Unknown Album", pal.text3
		}
		if cols.showArtist {
			tt := u.cell(gtx, tr.Info.Title, textStyle{size: fsBody, weight: font.Medium, color: titleCol}, image.Pt(x, 0), cols.title, rowH, bg)
			hoverCell(x, x+cols.title, tr.Info.Title, tt)
		} else {
			// Two lines: the title with the artist under it.
			lh := dp(gtx, 20)
			top := (rowH - 2*lh) / 2
			tt := u.cell(gtx, tr.Info.Title, textStyle{size: fsBody, weight: font.Medium, color: titleCol}, image.Pt(x, top), cols.title, lh, bg)
			at2 := u.cell(gtx, artist, textStyle{size: fsCell, color: artistCol}, image.Pt(x, top+lh), cols.title, lh, bg)
			if rs.hoverPos.Y < float32(top+lh) {
				hoverCell(x, x+cols.title, tr.Info.Title, tt)
			} else {
				hoverCell(x, x+cols.title, artist, at2)
			}
		}
		x += cols.title + cols.gap
		if cols.showArtist {
			tt := u.cell(gtx, artist, textStyle{size: fsCell, color: artistCol}, image.Pt(x, 0), cols.artist, rowH, bg)
			hoverCell(x, x+cols.artist, artist, tt)
			x += cols.artist + cols.gap
		}
		if cols.showAlbum {
			tt := u.cell(gtx, album, textStyle{size: fsCell, color: albumCol}, image.Pt(x, 0), cols.album, rowH, bg)
			hoverCell(x, x+cols.album, album, tt)
			x += cols.album + cols.gap
		}
	}
	durX := w - cols.pad - cols.more - cols.dur
	if d := tr.Duration(); d != "" {
		c, dd := u.text(gtx, d, textStyle{size: fsCell, color: pal.text2})
		place(gtx.Ops, image.Pt(durX+cols.dur-dd.Size.X, (rowH-dd.Size.Y)/2), c)
	}

	// The "⋯" button (same menu as a right click).
	if (rs.hovered || menuOpen) && !u.dnd.active {
		bx := w - cols.pad - dp(gtx, 28) - dp(gtx, 4)
		func() {
			defer u.push(gtx, image.Pt(bx, (rowH-dp(gtx, 28))/2))()
			st := iconBtn(icMore, 18)
			st.size = 28
			if _, clicked := rs.more.layout(u, gtx, st); clicked {
				if !t.sel[tr.ID] {
					t.selectOnly(i)
				}
				at := u.wr(image.Rect(0, dp(gtx, 28), 0, dp(gtx, 28))).Min
				u.openTrackMenu(t, at)
			}
		}()
	}
}

func bgOrNone(c color.NRGBA) color.NRGBA {
	if c == pal.bg {
		return color.NRGBA{}
	}
	return c
}

func skeleton() color.NRGBA {
	if pal.light {
		return pal.divider
	}
	return pal.hover
}

// rowInput handles clicks, double clicks, right clicks and drags on a row.
func (u *UI) rowInput(gtx layout.Context, t *tableState, i int, rs *rowState, w, rowH int) {
	tr := t.spec.tracks[i]
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: rs,
			Kinds: pointer.Press | pointer.Release | pointer.Drag | pointer.Enter | pointer.Leave | pointer.Move | pointer.Cancel})
		if !ok {
			break
		}
		e, ok := ev.(pointer.Event)
		if !ok {
			continue
		}
		switch e.Kind {
		case pointer.Enter, pointer.Move:
			rs.hovered, rs.hoverPos = true, e.Position
		case pointer.Leave:
			rs.hovered = false
		case pointer.Press:
			if e.Buttons.Contain(pointer.ButtonSecondary) {
				if !t.sel[tr.ID] {
					t.selectOnly(i)
				}
				u.openTrackMenu(t, u.mouse.pos)
				continue
			}
			if !e.Buttons.Contain(pointer.ButtonPrimary) {
				continue
			}
			rs.pressed, rs.pressPos = true, e.Position
			switch {
			case e.Modifiers.Contain(key.ModShortcut):
				if t.sel[tr.ID] {
					delete(t.sel, tr.ID)
				} else {
					t.sel[tr.ID] = true
				}
				t.anchor = i
			case e.Modifiers.Contain(key.ModShift) && t.anchor >= 0:
				t.selectRange(t.anchor, i)
			case t.sel[tr.ID]:
				rs.deferSel = true // keep a multiple selection for dragging; reduce on release
			default:
				t.selectOnly(i)
			}
			// Double click plays (with the list as the context).
			if t.lastClickID == tr.ID && gtx.Now.Sub(t.lastClickAt) < 400*time.Millisecond &&
				!e.Modifiers.Contain(key.ModShortcut|key.ModShift) {
				t.lastClickID = ""
				u.later(func() { u.playFromTable(t, i) })
			} else {
				t.lastClickID, t.lastClickAt = tr.ID, gtx.Now
			}
		case pointer.Drag:
			if rs.pressed && !u.dnd.active {
				d := e.Position.Sub(rs.pressPos)
				if d.X*d.X+d.Y*d.Y > float32(dp(gtx, 6)*dp(gtx, 6)) {
					if !t.sel[tr.ID] {
						t.selectOnly(i)
					}
					rs.deferSel = false
					u.startDrag(t.selectedIDs(), fromTable, t.spec.playlist, t.selectedIndexes())
				}
			}
		case pointer.Release:
			if u.dnd.active {
				u.later(u.finishDrag)
			} else if rs.deferSel {
				t.selectOnly(i)
			}
			rs.pressed, rs.deferSel = false, false
		case pointer.Cancel:
			if u.dnd.active {
				u.dnd = dragState{}
			}
			rs.pressed, rs.deferSel, rs.hovered = false, false, false
		}
	}
	defer clip.Rect(image.Rect(0, 0, w, rowH)).Push(gtx.Ops).Pop()
	event.Op(gtx.Ops, rs)
}

// playFromTable plays row i with the table as the context.
func (u *UI) playFromTable(t *tableState, i int) {
	if i < 0 || i >= len(t.spec.tracks) {
		return
	}
	u.p.PlayContext(t.spec.ctxName, t.spec.ctxRef, library.IDs(t.spec.tracks), i)
}

// tableKey handles keyboard commands for the table on screen.
func (u *UI) tableKey(t *tableState, e key.Event) {
	n := len(t.spec.tracks)
	if n == 0 {
		return
	}
	shift := e.Modifiers.Contain(key.ModShift)
	ctrl := e.Modifiers.Contain(key.ModShortcut)
	move := func(to int) {
		to = max(0, min(n-1, to))
		if shift && t.anchor >= 0 {
			t.selectRange(t.anchor, to)
		} else {
			t.selectOnly(to)
		}
		t.anchor = to
		first := t.list.Position.First
		if to < first {
			t.list.Position.First, t.list.Position.Offset = to, 0
		} else if to >= first+8 {
			t.list.ScrollTo(to)
		}
	}
	ids := t.selectedIDs()
	switch e.Name {
	case key.NameUpArrow:
		move(t.anchor - 1)
	case key.NameDownArrow:
		move(t.anchor + 1)
	case key.NameReturn, key.NameEnter:
		switch {
		case ctrl && shift && len(ids) > 0:
			u.p.PlayNext(ids)
		case t.anchor >= 0:
			u.playFromTable(t, t.anchor)
		}
	case "A":
		for _, tr := range t.spec.tracks {
			t.sel[tr.ID] = true
		}
	case "Q":
		if len(ids) > 0 {
			u.p.AddToQueue(ids)
		}
	case "I":
		if len(ids) == 1 {
			u.openTagEditor(u.lib.Track(ids[0]))
		}
	case "E":
		if len(ids) == 1 && u.host.ShowInExplorer != nil {
			u.host.ShowInExplorer(u.lib.Track(ids[0]).Path)
		}
	case key.NameDeleteForward:
		if len(ids) == 0 {
			return
		}
		if t.spec.playlist != "" {
			u.lib.RemoveFromPlaylist(t.spec.playlist, t.selectedIndexes())
			clear(t.sel)
		} else {
			u.confirmRemove(t, ids)
		}
	}
}

func (u *UI) confirmRemove(t *tableState, ids []string) {
	remove := func() {
		u.p.RemoveFromLibrary(ids)
		clear(t.sel)
	}
	if len(ids) == 1 {
		remove()
		return
	}
	u.openConfirm("Remove "+countLabel(len(ids), "track")+" from the library?",
		"The files stay on disk. Tracks inside watched folders will not be added back on rescans.", "Remove", remove)
}

// openTrackMenu is the right-click / "⋯" menu for the table's selection.
func (u *UI) openTrackMenu(t *tableState, at image.Point) {
	ids := t.selectedIDs()
	if len(ids) == 0 {
		return
	}
	first := u.lib.Track(ids[0])
	header := ""
	if len(ids) > 1 {
		header = countLabel(len(ids), "track")
	}
	single := len(ids) == 1
	items := []menuItem{
		{label: "Play Next", glyph: icPlayNext, shortcut: "Ctrl+Shift+Enter", do: func() { u.p.PlayNext(ids) }},
		{label: "Add to Queue", glyph: icAddQueue, shortcut: "Ctrl+Q", do: func() { u.p.AddToQueue(ids) }},
		{label: "Add to Playlist", glyph: icPlaylist, sub: func() []menuItem { return u.playlistSubmenu(ids) }},
		{separator: true},
	}
	if first != nil {
		items = append(items,
			menuItem{label: "Go to Artist", glyph: icArtist, do: func() {
				if ar := u.lib.ArtistOf(first); ar != nil {
					u.navigate(route{kind: routeArtist, key: ar.Key})
				}
			}},
			menuItem{label: "Go to Album", glyph: icAlbum, do: func() {
				if al := u.lib.AlbumOf(first); al != nil {
					u.navigate(route{kind: routeAlbum, key: al.Key})
				}
			}},
			menuItem{separator: true},
			menuItem{label: "Show in File Explorer", glyph: icExplorer, shortcut: "Ctrl+Shift+E", disabled: !single,
				do: func() {
					if u.host.ShowInExplorer != nil {
						u.host.ShowInExplorer(first.Path)
					}
				}},
			menuItem{label: "Edit Metadata…", glyph: icEdit, shortcut: "Ctrl+I", disabled: !single,
				do: func() { u.openTagEditor(first) }},
			menuItem{separator: true},
		)
	}
	if t.spec.playlist != "" {
		name := t.spec.playlist
		idx := t.selectedIndexes()
		items = append(items, menuItem{label: "Remove from Playlist", glyph: icClose, shortcut: "Delete", do: func() {
			u.lib.RemoveFromPlaylist(name, idx)
			clear(t.sel)
		}})
	}
	shortcut := "Delete"
	if t.spec.playlist != "" {
		shortcut = ""
	}
	items = append(items, menuItem{label: "Remove from Library", glyph: icDelete, shortcut: shortcut, danger: true,
		do: func() { u.confirmRemove(t, ids) }})
	u.openMenu(at, header, items)
}

// playlistSubmenu lists "New playlist…" and the user's playlists as destinations.
func (u *UI) playlistSubmenu(ids []string) []menuItem {
	items := []menuItem{{label: "New playlist…", glyph: icAdd, do: func() {
		u.openNameDialog("New playlist", "Create", "", func(name string) {
			name = u.lib.NewPlaylist(name, ids)
			u.p.OnToast(toastf("Created %s", name))
		})
	}}}
	if pls := u.lib.Playlists(); len(pls) > 0 {
		items = append(items, menuItem{separator: true})
		for _, pl := range pls {
			name := pl.Name
			items = append(items, menuItem{label: name, glyph: icPlaylist, do: func() {
				u.lib.AddToPlaylist(name, ids, -1)
				u.p.OnToast(toastf("Added %s to %s", describeIDs(u.lib, ids), name))
			}})
		}
	}
	return items
}
