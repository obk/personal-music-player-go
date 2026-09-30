package ui

import (
	"image"
	"math"
	"time"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/widget"

	"musicplayer/internal/library"
)

// drawerState is the queue drawer: Now Playing, Up Next (hand-queued, reorderable) and the rest of the list
// playback came from.
type drawerState struct {
	list     widget.List
	clearBtn button
	rows     map[string]*queueRow
	rect     image.Rectangle // window coordinates, for drops from Explorer
	upTop    int             // window y of the first Up Next row, and the row height (for drop positions)
	rowH     int
	upCount  int
	lastTop  int
}

type queueRow struct {
	hovered  bool
	pressed  bool
	pressPos f32.Point
	lastAt   time.Time
	remove   iconButton
}

// dropIndexAt returns the Up Next insertion index for a window y.
func (d *drawerState) dropIndexAt(y int) int {
	if d.rowH <= 0 {
		return d.upCount
	}
	return max(0, min(d.upCount, int(math.Round(float64(y-d.upTop)/float64(d.rowH)))))
}

type queueItem struct {
	kind    int // 0 heading, 1 now playing, 2 up next, 3 from context, 4 empty note
	text    string
	id      string
	index   int
	heading bool
}

func (u *UI) layoutDrawer(gtx layout.Context, g geometry) {
	d := &u.drawer
	if d.rows == nil {
		d.rows = map[string]*queueRow{}
	}
	r := g.drawer
	d.rect = r
	q := u.p.Queue()
	if g.overlay {
		shadow(gtx.Ops, r, dp(gtx, rPanel), dp(gtx, 24), 0)
		fillRRect(gtx.Ops, image.Rect(r.Min.X, r.Min.Y, r.Max.X+dp(gtx, rPanel), r.Max.Y), dp(gtx, rPanel), pal.surface)
	} else {
		fillRect(gtx.Ops, r, pal.surface)
	}
	fillRect(gtx.Ops, image.Rect(r.Min.X, r.Min.Y, r.Min.X+1, r.Max.Y), pal.divider)
	// Swallow input on the drawer (it may float above the main view).
	func() {
		defer clip.Rect(r).Push(gtx.Ops).Pop()
		event.Op(gtx.Ops, d)
		for {
			if _, ok := gtx.Event(pointer.Filter{Target: d, Kinds: pointer.Press}); !ok {
				break
			}
		}
	}()
	defer u.push(gtx, r.Min)()
	w := r.Dx()
	pad := dp(gtx, 16)

	// Header: title and Clear (the window controls sit in the top-right corner above it).
	hh := dp(gtx, 72)
	tc, td := u.text(gtx, "Queue", textStyle{size: 20, weight: font.SemiBold, color: pal.text1})
	place(gtx.Ops, image.Pt(pad, dp(gtx, 36)+(dp(gtx, 32)-td.Size.Y)/2), tc)
	if len(q.UpNext()) > 0 {
		cw := u.buttonWidth(gtx, "Clear", icClose) - dp(gtx, 6) - dp(gtx, 18)
		u.at(gtx, image.Pt(w-pad-cw, dp(gtx, 36)), image.Pt(cw, dp(gtx, 32)), func(gtx layout.Context) layout.Dimensions {
			dd, clicked := d.clearBtn.layout(u, gtx, "Clear", "", btnGhost, 32)
			if clicked {
				u.later(u.p.Queue().ClearUpNext)
			}
			return dd
		})
	}
	moveArea(gtx, image.Rect(0, 0, w-dp(gtx, 3*46), dp(gtx, 32)))

	// Items.
	var items []queueItem
	cur := q.Current()
	if cur != "" {
		items = append(items, queueItem{kind: 0, text: "Now Playing"}, queueItem{kind: 1, id: cur})
	}
	up := q.UpNext()
	items = append(items, queueItem{kind: 0, text: "Up Next"})
	upStart := len(items)
	for i, id := range up {
		items = append(items, queueItem{kind: 2, id: id, index: i})
	}
	if len(up) == 0 {
		items = append(items, queueItem{kind: 4, text: "Drag tracks here, or use Play Next / Add to Queue."})
	}
	rest := q.ContextRest()
	if len(rest) > 0 {
		items = append(items, queueItem{kind: 0, text: "From: " + q.Context().Name})
		for i, id := range rest {
			items = append(items, queueItem{kind: 3, id: id, index: i})
		}
	}

	headH, rowH, bigH, noteH := dp(gtx, 36), dp(gtx, 48), dp(gtx, 64), dp(gtx, 44)
	height := func(it queueItem) int {
		switch it.kind {
		case 0:
			return headH
		case 1:
			return bigH
		case 4:
			return noteH
		}
		return rowH
	}
	listR := image.Rect(0, hh, w, r.Dy())
	d.list.Axis = layout.Vertical
	d.rowH, d.upCount = rowH, len(up)
	// Window y of the first Up Next row (items before it have fixed heights).
	yUp := 0
	for _, it := range items[:upStart] {
		yUp += height(it)
	}
	func() {
		defer u.push(gtx, listR.Min)()
		defer clip.Rect(image.Rectangle{Max: listR.Size()}).Push(gtx.Ops).Pop()
		gg := gtx
		gg.Constraints = layout.Exact(listR.Size())
		org := u.org
		// Scroll offset in pixels of the list (items have varying heights: sum those above First).
		scroll := d.list.Position.Offset
		for i := 0; i < d.list.Position.First && i < len(items); i++ {
			scroll += height(items[i])
		}
		d.upTop = org.Y + yUp - scroll
		d.list.Layout(gg, len(items), func(gtx layout.Context, i int) layout.Dimensions {
			it := items[i]
			y := -scroll
			for j := 0; j < i; j++ {
				y += height(items[j])
			}
			u.org = org.Add(image.Pt(0, y))
			defer func() { u.org = org }()
			h := height(it)
			switch it.kind {
			case 0:
				c, dd := u.text(gtx, it.text, textStyle{size: fsColumn, weight: font.SemiBold, color: pal.text3})
				u.cell(gtx, it.text, textStyle{size: fsColumn, weight: font.SemiBold, color: pal.text3},
					image.Pt(pad, h-dd.Size.Y-dp(gtx, 8)), w-2*pad, dd.Size.Y, pal.surface)
				_ = c
			case 4:
				u.cell(gtx, it.text, textStyle{size: fsCaption, color: pal.text3}, image.Pt(pad, 0), w-2*pad, dp(gtx, 32), pal.surface)
			default:
				u.queueRow(gtx, it, w, h)
			}
			return layout.Dimensions{Size: image.Pt(w, h)}
		})
		// Dragging tracks over the drawer: an insertion line in Up Next.
		if u.dnd.active && u.dnd.pos.In(u.wr(image.Rectangle{Max: listR.Size()})) {
			idx := d.dropIndexAt(u.dnd.pos.Y)
			ly := d.upTop - org.Y + idx*rowH
			fillRect(gtx.Ops, image.Rect(pad, ly-dp(gtx, 1), w-pad, ly+dp(gtx, 1)), pal.accent)
			u.dnd.target = dropTarget{kind: dropUpNext, index: idx}
		}
	}()
}

func (u *UI) queueRow(gtx layout.Context, it queueItem, w, h int) {
	d := &u.drawer
	key := [...]string{"", "now:", "up:", "ctx:", ""}[it.kind] + formatInt(int64(it.index)) + ":" + it.id
	qr := d.rows[key]
	if qr == nil {
		qr = &queueRow{}
		d.rows[key] = qr
	}
	t := u.lib.Track(it.id)
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: qr,
			Kinds: pointer.Press | pointer.Release | pointer.Drag | pointer.Enter | pointer.Leave | pointer.Cancel})
		if !ok {
			break
		}
		e, ok := ev.(pointer.Event)
		if !ok {
			continue
		}
		switch e.Kind {
		case pointer.Enter:
			qr.hovered = true
		case pointer.Leave:
			qr.hovered = false
		case pointer.Press:
			if e.Buttons.Contain(pointer.ButtonSecondary) && t != nil {
				u.openQueueMenu(it, u.mouse.pos)
				continue
			}
			qr.pressed, qr.pressPos = true, e.Position
			if gtx.Now.Sub(qr.lastAt) < 400*time.Millisecond { // double click: play
				qr.lastAt = time.Time{}
				item := it
				u.later(func() {
					switch item.kind {
					case 2:
						u.p.Queue().PlayUpNextAt(item.index)
					case 3:
						u.p.Queue().PlayContextRestAt(item.index)
					}
				})
			} else {
				qr.lastAt = gtx.Now
			}
		case pointer.Drag:
			dd := e.Position.Sub(qr.pressPos)
			if qr.pressed && it.kind == 2 && !u.dnd.active && dd.X*dd.X+dd.Y*dd.Y > float32(dp(gtx, 6)*dp(gtx, 6)) {
				u.startDrag([]string{it.id}, fromUpNext, "", []int{it.index})
			}
		case pointer.Release:
			if u.dnd.active {
				u.later(u.finishDrag)
			}
			qr.pressed = false
		case pointer.Cancel:
			qr.pressed, qr.hovered = false, false
		}
	}
	if t == nil {
		return
	}
	if qr.hovered && !u.dnd.active {
		fillRRect(gtx.Ops, image.Rect(dp(gtx, 8), 0, w-dp(gtx, 8), h), dp(gtx, rSmall), pal.hover)
	}
	bg := pal.surface
	if qr.hovered && !u.dnd.active {
		bg = pal.hover
	}
	a := dp(gtx, 36)
	if it.kind == 1 {
		a = dp(gtx, 48)
	}
	pad := dp(gtx, 16)
	ar := image.Rect(pad, (h-a)/2, pad+a, (h+a)/2)
	u.artwork(gtx, u.coverFor(t.Path, t.HasCover, a), albumKeyOf(t), ar, dp(gtx, rSmall))
	tx := ar.Max.X + dp(gtx, 12)
	right := w - pad
	if qr.hovered && it.kind == 2 {
		right -= dp(gtx, 28)
		func() {
			defer u.push(gtx, image.Pt(right, (h-dp(gtx, 28))/2))()
			st := iconBtn(icClose, 16)
			st.size = 28
			if _, clicked := qr.remove.layout(u, gtx, st); clicked {
				i := it.index
				u.later(func() { u.p.Queue().RemoveUpNext([]int{i}) })
			}
		}()
		right -= dp(gtx, 4)
	} else if dur := t.Duration(); dur != "" {
		c, dd := u.text(gtx, dur, textStyle{size: fsCaption, color: pal.text2})
		right -= dd.Size.X
		place(gtx.Ops, image.Pt(right, (h-dd.Size.Y)/2), c)
		right -= dp(gtx, 12)
	}
	titleCol, subCol := pal.text1, pal.text2
	if it.kind == 1 {
		titleCol = pal.accent
	}
	if it.kind == 3 {
		titleCol = pal.text2
		subCol = pal.text3
	}
	lh := dp(gtx, 18)
	top := (h - 2*lh) / 2
	u.cell(gtx, t.Info.Title, textStyle{size: fsCell, weight: font.Medium, color: titleCol}, image.Pt(tx, top), right-tx, lh, bg)
	u.cell(gtx, orUnknown(t.Info.Artist, "Unknown Artist"), textStyle{size: fsCaption, color: subCol},
		image.Pt(tx, top+lh), right-tx, lh, bg)
	defer clip.Rect(image.Rect(0, 0, w, h)).Push(gtx.Ops).Pop()
	defer pointer.PassOp{}.Push(gtx.Ops).Pop()
	event.Op(gtx.Ops, qr)
}

func albumKeyOf(t *library.Track) string { return t.Info.Album + "\x00" + t.ArtistName() }

func (u *UI) openQueueMenu(it queueItem, at image.Point) {
	ids := []string{it.id}
	t := u.lib.Track(it.id)
	items := []menuItem{}
	if it.kind == 2 {
		i := it.index
		items = append(items,
			menuItem{label: "Play", glyph: icPlay, do: func() { u.p.Queue().PlayUpNextAt(i) }},
			menuItem{label: "Remove from Queue", glyph: icClose, do: func() { u.p.Queue().RemoveUpNext([]int{i}) }},
			menuItem{separator: true})
	}
	if it.kind == 3 {
		i := it.index
		items = append(items, menuItem{label: "Play", glyph: icPlay, do: func() { u.p.Queue().PlayContextRestAt(i) }},
			menuItem{label: "Play Next", glyph: icPlayNext, do: func() { u.p.PlayNext(ids) }}, menuItem{separator: true})
	}
	items = append(items,
		menuItem{label: "Add to Playlist", glyph: icPlaylist, sub: func() []menuItem { return u.playlistSubmenu(ids) }},
		menuItem{label: "Go to Album", glyph: icAlbum, do: func() {
			if al := u.lib.AlbumOf(t); al != nil {
				u.navigate(route{kind: routeAlbum, key: al.Key})
			}
		}},
		menuItem{label: "Show in File Explorer", glyph: icExplorer, do: func() {
			if u.host.ShowInExplorer != nil {
				u.host.ShowInExplorer(t.Path)
			}
		}})
	u.openMenu(at, "", items)
}
