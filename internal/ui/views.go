package ui

import (
	"image"
	"math/rand/v2"
	"path/filepath"

	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/widget"

	"musicplayer/internal/library"
)

// ---- page header ----

type headerSpec struct {
	title   string
	meta    string
	back    bool
	art     bool // album header with a 128 dp cover
	artImg  string
	artHas  bool
	artKey  string
	ids     []string
	ctxName string
	ctxRef  string
	menu    func(at image.Point) // the header's "⋯" menu, or nil
}

type headerState struct {
	back    iconButton
	play    button
	shuffle iconButton
	more    iconButton
}

// pageHeader draws a view's header at the top of r and returns its height. Its empty area moves the window.
func (u *UI) pageHeader(gtx layout.Context, r image.Rectangle, spec headerSpec, reserveRight int) int {
	s := &u.header
	h := dp(gtx, 72)
	if spec.art {
		h = dp(gtx, 176)
	}
	pad := dp(gtx, 24)
	x := r.Min.X + pad
	if spec.back {
		func() {
			defer u.push(gtx, image.Pt(r.Min.X+dp(gtx, 12), r.Min.Y+(dp(gtx, 72)-dp(gtx, 32))/2))()
			st := iconBtn(icBack, 20)
			st.size = 32
			if _, clicked := s.back.layout(u, gtx, st); clicked {
				u.later(u.goBack)
			}
		}()
		x = r.Min.X + dp(gtx, 52)
	}
	textTop := r.Min.Y
	textH := dp(gtx, 72)
	if spec.art {
		ar := image.Rect(x, r.Min.Y+pad, x+dp(gtx, 128), r.Min.Y+pad+dp(gtx, 128))
		u.artwork(gtx, u.coverFor(spec.artImg, spec.artHas, dp(gtx, 128)), spec.artKey, ar, dp(gtx, rArt))
		x = ar.Max.X + pad
		textTop, textH = ar.Min.Y, dp(gtx, 72)
	}

	// Buttons on the right (below the title for the album header).
	right := r.Max.X - pad - reserveRight
	by := r.Min.Y + (dp(gtx, 72)-dp(gtx, 36))/2
	bx := right
	if spec.art {
		bx = x + dp(gtx, 4)
		by = textTop + textH + dp(gtx, 16)
	}
	var btns []func(x int) int // each draws at x and returns its width
	if len(spec.ids) > 0 {
		btns = append(btns, func(x int) int {
			d := u.at(gtx, image.Pt(x, by), image.Pt(dp(gtx, 200), dp(gtx, 36)), func(gtx layout.Context) layout.Dimensions {
				d, clicked := s.play.layout(u, gtx, "Play", icPlay, btnPrimary, 36)
				if clicked {
					ids, name, ref := spec.ids, spec.ctxName, spec.ctxRef
					u.later(func() { u.p.PlayContext(name, ref, ids, 0) })
				}
				return d
			})
			return d.Size.X
		}, func(x int) int {
			func() {
				defer u.push(gtx, image.Pt(x, by+dp(gtx, 2)))()
				st := iconBtn(icShuffle, 20)
				st.size = 32
				if _, clicked := s.shuffle.layout(u, gtx, st); clicked {
					ids, name, ref := spec.ids, spec.ctxName, spec.ctxRef
					u.later(func() {
						if !u.p.Shuffle() {
							u.p.ToggleShuffle()
						}
						u.p.PlayContext(name, ref, ids, rand.IntN(len(ids)))
					})
				}
			}()
			return dp(gtx, 32)
		})
	}
	if spec.menu != nil {
		btns = append(btns, func(x int) int {
			func() {
				defer u.push(gtx, image.Pt(x, by+dp(gtx, 2)))()
				st := iconBtn(icMore, 20)
				st.size = 32
				if _, clicked := s.more.layout(u, gtx, st); clicked {
					spec.menu(u.wr(image.Rect(0, dp(gtx, 34), 0, 0)).Min)
				}
			}()
			return dp(gtx, 32)
		})
	}
	gap := dp(gtx, 8)
	if spec.art {
		for _, b := range btns {
			bx += b(bx) + gap
		}
	} else {
		// Measure right to left: play is 36 dp tall and ~90 dp wide; draw left to right from the computed start.
		widths := []int{}
		for range btns {
			widths = append(widths, 0)
		}
		total := 0
		for i := range btns {
			if i == 0 && len(spec.ids) > 0 {
				widths[i] = u.buttonWidth(gtx, "Play", icPlay)
			} else {
				widths[i] = dp(gtx, 32)
			}
			total += widths[i] + gap
		}
		bx = right - total + gap
		for i, b := range btns {
			b(bx)
			bx += widths[i] + gap
		}
		right = right - total
	}

	// Title and meta.
	tw := right - x - dp(gtx, 16)
	if spec.art {
		tw = r.Max.X - pad - reserveRight - x
	}
	titleH := dp(gtx, 36)
	metaH := dp(gtx, 20)
	ty := textTop + (textH-titleH-metaH)/2
	if spec.meta == "" {
		ty = textTop + (textH-titleH)/2
	}
	if u.cell(gtx, spec.title, textStyle{size: fsPageHeader, weight: font.SemiBold, color: pal.text1},
		image.Pt(x, ty), tw, titleH, pal.bg) {
		if p := u.mouse.pos; p.In(u.wr(image.Rect(x, ty, x+tw, ty+titleH))) {
			u.tipAtMouse(&u.header, spec.title)
		}
	}
	if spec.meta != "" {
		u.cell(gtx, spec.meta, textStyle{size: fsCell, color: pal.text2}, image.Pt(x, ty+titleH), tw, metaH, pal.bg)
	}
	// The title area moves the window (frameless).
	moveArea(gtx, image.Rect(x, r.Min.Y, max(x, right-dp(gtx, 8)), r.Min.Y+dp(gtx, 72)))
	return h
}

func (u *UI) buttonWidth(gtx layout.Context, label, glyph string) int {
	_, td := u.text(gtx, label, textStyle{size: fsBody, weight: font.SemiBold})
	_, id := u.icon(gtx, glyph, 18, pal.text1)
	return td.Size.X + 2*dp(gtx, 16) + id.Size.X + dp(gtx, 6)
}

func tracksMeta(ts []*library.Track) string {
	if len(ts) == 0 {
		return "No tracks"
	}
	return groupDigits(len(ts)) + map[bool]string{true: " track", false: " tracks"}[len(ts) == 1] + " · " +
		durationLabel(library.TotalDuration(ts))
}

// ---- main view ----

func (u *UI) layoutMain(gtx layout.Context, g geometry) {
	r := g.main
	defer clip.Rect(r).Push(gtx.Ops).Pop()
	// Keep the header's buttons clear of the window controls when the header reaches the window's edge.
	reserve := 0
	if r.Max.X >= g.ws.X-1 || g.overlay {
		reserve = dp(gtx, 3*46) - dp(gtx, 24) + dp(gtx, 8)
	}
	rt := u.route
	libEmpty := u.lib.Count() == 0 && !u.lib.Scanning()
	switch rt.kind {
	case routeNowPlaying:
		moveArea(gtx, image.Rect(r.Min.X, r.Min.Y, r.Max.X-dp(gtx, 3*46), r.Min.Y+dp(gtx, 40)))
		u.layoutNowPlaying(gtx, r.Inset(dp(gtx, 24)).Add(image.Pt(0, dp(gtx, 8))))
		return
	case routeSongs, routeAlbums, routeArtists, routeFolders:
		if libEmpty {
			u.emptyLibrary(gtx, r, reserve)
			return
		}
	}
	switch rt.kind {
	case routeSongs:
		songs := u.lib.Songs()
		meta := tracksMeta(songs)
		if u.lib.Scanning() {
			meta += " · Scanning…"
		}
		u.tableView(gtx, r, reserve, headerSpec{title: "Songs", meta: meta, ids: library.IDs(songs), ctxName: "Songs",
			ctxRef: "songs"}, tableSpec{key: rt.ref(), tracks: songs, ctxName: "Songs", ctxRef: "songs"})
	case routeLiked:
		ts := u.lib.Liked()
		u.tableView(gtx, r, reserve, headerSpec{title: "Liked Songs", meta: tracksMeta(ts), ids: library.IDs(ts),
			ctxName: "Liked Songs", ctxRef: "liked", menu: func(at image.Point) { u.openPlaylistMenu("", true, at) }},
			tableSpec{key: rt.ref(), tracks: ts, ctxName: "Liked Songs", ctxRef: "liked"})
	case routeSearch:
		q := u.search.query()
		ts := u.lib.Search(q)
		meta := "No results for “" + q + "”"
		if len(ts) > 0 {
			meta = countLabel(len(ts), "result") + " for “" + q + "”"
		}
		u.tableView(gtx, r, reserve, headerSpec{title: "Search", meta: meta, ids: library.IDs(ts),
			ctxName: "Search: " + q, ctxRef: "search:" + q},
			tableSpec{key: rt.ref(), tracks: ts, ctxName: "Search: " + q, ctxRef: "search:" + q})
	case routePlaylist:
		pl := u.lib.Playlist(rt.key)
		if pl == nil {
			u.route = route{kind: routeSongs}
			return
		}
		ts := u.lib.Resolve(pl.IDs)
		name := pl.Name
		u.tableView(gtx, r, reserve, headerSpec{title: name, meta: "Playlist · " + tracksMeta(ts), ids: pl.IDs,
			ctxName: name, ctxRef: rt.ref(), menu: func(at image.Point) { u.openPlaylistMenu(name, false, at) }},
			tableSpec{key: rt.ref(), tracks: ts, ctxName: name, ctxRef: rt.ref(), playlist: name})
	case routeAlbum:
		al := u.lib.Album(rt.key)
		if al == nil {
			u.route = route{kind: routeAlbums}
			return
		}
		title, artist := orUnknown(al.Title, "Unknown Album"), orUnknown(al.Artist, "Unknown Artist")
		meta := artist
		if al.Year > 0 {
			meta += " · " + formatInt(int64(al.Year))
		}
		meta += " · " + tracksMeta(al.Tracks)
		ct := al.CoverTrack()
		u.tableView(gtx, r, reserve, headerSpec{title: title, meta: meta, back: true, art: true, artImg: ct.Path,
			artHas: ct.HasCover, artKey: al.Key, ids: library.IDs(al.Tracks), ctxName: title, ctxRef: rt.ref()},
			tableSpec{key: rt.ref(), tracks: al.Tracks, ctxName: title, ctxRef: rt.ref(), albumNums: true})
	case routeArtist:
		ar := u.lib.Artist(rt.key)
		if ar == nil {
			u.route = route{kind: routeArtists}
			return
		}
		name := orUnknown(ar.Name, "Unknown Artist")
		meta := countLabel(ar.Albums, "album") + " · " + tracksMeta(ar.Tracks)
		u.tableView(gtx, r, reserve, headerSpec{title: name, meta: meta, back: true, ids: library.IDs(ar.Tracks),
			ctxName: name, ctxRef: rt.ref()}, tableSpec{key: rt.ref(), tracks: ar.Tracks, ctxName: name, ctxRef: rt.ref()})
	case routeFolder:
		fo := u.lib.Folder(rt.key)
		if fo == nil {
			u.route = route{kind: routeFolders}
			return
		}
		name := filepath.Base(fo.Path)
		u.tableView(gtx, r, reserve, headerSpec{title: name, meta: fo.Path + " · " + tracksMeta(fo.Tracks), back: true,
			ids: library.IDs(fo.Tracks), ctxName: name, ctxRef: rt.ref()},
			tableSpec{key: rt.ref(), tracks: fo.Tracks, ctxName: name, ctxRef: rt.ref()})
	case routeAlbums:
		albums := u.lib.Albums()
		hh := u.pageHeader(gtx, r, headerSpec{title: "Albums", meta: countLabel(len(albums), "album")}, reserve)
		u.layoutAlbumGrid(gtx, image.Rect(r.Min.X, r.Min.Y+hh, r.Max.X, r.Max.Y), albums)
	case routeArtists:
		artists := u.lib.Artists()
		hh := u.pageHeader(gtx, r, headerSpec{title: "Artists", meta: countLabel(len(artists), "artist")}, reserve)
		u.layoutArtistList(gtx, image.Rect(r.Min.X, r.Min.Y+hh, r.Max.X, r.Max.Y), artists)
	case routeFolders:
		folders := u.lib.FolderList()
		hh := u.pageHeader(gtx, r, headerSpec{title: "Folders", meta: countLabel(len(folders), "folder")}, reserve)
		u.layoutFolderList(gtx, image.Rect(r.Min.X, r.Min.Y+hh, r.Max.X, r.Max.Y), folders)
	}
}

func orUnknown(s, unknown string) string {
	if s == "" {
		return unknown
	}
	return s
}

func (u *UI) tableView(gtx layout.Context, r image.Rectangle, reserve int, h headerSpec, t tableSpec) {
	hh := u.pageHeader(gtx, r, h, reserve)
	u.layoutTable(gtx, image.Rect(r.Min.X, r.Min.Y+hh, r.Max.X, r.Max.Y), t)
}

type emptyState struct {
	add   button
	files button
}

var empty emptyState

func (u *UI) emptyLibrary(gtx layout.Context, r image.Rectangle, reserve int) {
	moveArea(gtx, image.Rect(r.Min.X, r.Min.Y, r.Max.X-reserve-dp(gtx, 24), r.Min.Y+dp(gtx, 72)))
	ic, id := u.icon(gtx, icSongs, 64, pal.text3)
	tc, td := u.text(gtx, "Your library is empty", textStyle{size: 20, weight: font.SemiBold, color: pal.text1})
	hc, hd := u.text(gtx, "Drop music folders or files anywhere in this window, or", textStyle{size: fsBody, color: pal.text2})
	total := id.Size.Y + dp(gtx, 16) + td.Size.Y + dp(gtx, 8) + hd.Size.Y + dp(gtx, 20) + dp(gtx, 36)
	y := r.Min.Y + (r.Dy()-total)/2
	cx := r.Min.X + r.Dx()/2
	place(gtx.Ops, image.Pt(cx-id.Size.X/2, y), ic)
	y += id.Size.Y + dp(gtx, 16)
	place(gtx.Ops, image.Pt(cx-td.Size.X/2, y), tc)
	y += td.Size.Y + dp(gtx, 8)
	place(gtx.Ops, image.Pt(cx-hd.Size.X/2, y), hc)
	y += hd.Size.Y + dp(gtx, 20)
	w1 := u.buttonWidth(gtx, "Add folder", icAddFolder)
	w2 := u.buttonWidth(gtx, "Add files", icAdd)
	x := cx - (w1+dp(gtx, 8)+w2)/2
	u.at(gtx, image.Pt(x, y), image.Pt(w1, dp(gtx, 36)), func(gtx layout.Context) layout.Dimensions {
		d, clicked := empty.add.layout(u, gtx, "Add folder", icAddFolder, btnPrimary, 36)
		if clicked && u.host.AddFolder != nil {
			u.host.AddFolder()
		}
		return d
	})
	u.at(gtx, image.Pt(x+w1+dp(gtx, 8), y), image.Pt(w2, dp(gtx, 36)), func(gtx layout.Context) layout.Dimensions {
		d, clicked := empty.files.layout(u, gtx, "Add files", icAdd, btnSecondary, 36)
		if clicked && u.host.AddFiles != nil {
			u.host.AddFiles()
		}
		return d
	})
}

// ---- album grid ----

type gridState struct {
	list  widget.List
	tiles map[string]*tileState
}

type tileState struct {
	click widget.Clickable
	play  iconButton
	dim   fade
}

func (u *UI) layoutAlbumGrid(gtx layout.Context, r image.Rectangle, albums []*library.Album) {
	g := &u.grid
	if g.tiles == nil {
		g.tiles = map[string]*tileState{}
	}
	pad, gap := dp(gtx, 24), dp(gtx, 24)
	avail := r.Dx() - 2*pad
	cols := max(1, (avail+gap)/(dp(gtx, 160)+gap))
	tile := min(dp(gtx, 220), (avail-gap*(cols-1))/cols)
	rowH := tile + dp(gtx, 8) + dp(gtx, 20) + dp(gtx, 18) + gap
	rows := (len(albums) + cols - 1) / cols
	g.list.Axis = layout.Vertical
	defer u.push(gtx, r.Min)()
	defer clip.Rect(image.Rectangle{Max: r.Size()}).Push(gtx.Ops).Pop()
	gg := gtx
	gg.Constraints = layout.Exact(r.Size())
	org := u.org
	g.list.Layout(gg, rows+1, func(gtx layout.Context, row int) layout.Dimensions {
		if row == rows {
			return layout.Dimensions{Size: image.Pt(r.Dx(), pad)} // bottom padding
		}
		y := (row-g.list.Position.First)*rowH - g.list.Position.Offset
		u.org = org.Add(image.Pt(0, y))
		defer func() { u.org = org }()
		for c := 0; c < cols; c++ {
			i := row*cols + c
			if i >= len(albums) {
				break
			}
			x := pad + c*(tile+gap)
			func() {
				defer u.push(gtx, image.Pt(x, 0))()
				u.albumTile(gtx, albums[i], tile)
			}()
		}
		return layout.Dimensions{Size: image.Pt(r.Dx(), rowH)}
	})
}

func (u *UI) albumTile(gtx layout.Context, al *library.Album, tile int) {
	ts := u.grid.tiles[al.Key]
	if ts == nil {
		ts = &tileState{}
		u.grid.tiles[al.Key] = ts
	}
	if ts.click.Clicked(gtx) {
		key := al.Key
		u.later(func() { u.navigate(route{kind: routeAlbum, key: key}) })
	}
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: ts, Kinds: pointer.Press})
		if !ok {
			break
		}
		if e, ok := ev.(pointer.Event); ok && e.Buttons.Contain(pointer.ButtonSecondary) {
			u.openCollectionMenu(orUnknown(al.Title, "Unknown Album"), route{kind: routeAlbum, key: al.Key}.ref(),
				library.IDs(al.Tracks), al.Tracks[0], u.mouse.pos)
		}
	}
	h := tile + dp(gtx, 8) + dp(gtx, 20) + dp(gtx, 18)
	art := image.Rect(0, 0, tile, tile)
	gg := gtx
	gg.Constraints = layout.Exact(image.Pt(tile, h))
	ts.click.Layout(gg, func(gtx layout.Context) layout.Dimensions {
		ct := al.CoverTrack()
		u.artwork(gtx, u.coverFor(ct.Path, ct.HasCover, tile), al.Key, art, dp(gtx, rArt))
		if a := ts.dim.to(boolf(ts.click.Hovered() || ts.play.click.Hovered()), fast); a > 0 {
			fillRRect(gtx.Ops, art, dp(gtx, rArt), withAlpha(argb(0x4D000000), a))
		}
		title := orUnknown(al.Title, "Unknown Album")
		titleCol := pal.text1
		if al.Title == "" {
			titleCol = pal.text3
		}
		if u.cell(gtx, title, textStyle{size: fsBody, weight: font.Medium, color: titleCol}, image.Pt(0, tile+dp(gtx, 8)),
			tile, dp(gtx, 20), pal.bg) && ts.click.Hovered() {
			u.tipAtMouse(ts, title)
		}
		sub := orUnknown(al.Artist, "Unknown Artist")
		if al.Year > 0 {
			sub += " · " + formatInt(int64(al.Year))
		}
		u.cell(gtx, sub, textStyle{size: fsCell, color: pal.text2}, image.Pt(0, tile+dp(gtx, 28)), tile, dp(gtx, 18), pal.bg)
		pointerCursor(gtx, image.Rect(0, 0, tile, h), pointer.CursorPointer)
		return layout.Dimensions{Size: image.Pt(tile, h)}
	})
	func() {
		defer clip.Rect(image.Rect(0, 0, tile, h)).Push(gtx.Ops).Pop()
		defer pointer.PassOp{}.Push(gtx.Ops).Pop()
		event.Op(gtx.Ops, ts)
	}()
	// Hover: a play button in the cover's corner.
	if ts.click.Hovered() || ts.play.click.Hovered() {
		b := dp(gtx, 40)
		func() {
			defer u.push(gtx, image.Pt(tile-b-dp(gtx, 8), tile-b-dp(gtx, 8)))()
			st := iconStyle{glyph: icPlay, iconSize: 24, size: 40, filled: true}
			if _, clicked := ts.play.layout(u, gtx, st); clicked {
				name, ref, ids := orUnknown(al.Title, "Unknown Album"), route{kind: routeAlbum, key: al.Key}.ref(), library.IDs(al.Tracks)
				u.later(func() { u.p.PlayContext(name, ref, ids, 0) })
			}
		}()
	}
}

// openCollectionMenu is the right-click menu of an album, artist or folder.
func (u *UI) openCollectionMenu(name, ref string, ids []string, first *library.Track, at image.Point) {
	items := []menuItem{
		{label: "Play", glyph: icPlay, do: func() { u.p.PlayContext(name, ref, ids, 0) }},
		{label: "Play Next", glyph: icPlayNext, do: func() { u.p.PlayNext(ids) }},
		{label: "Add to Queue", glyph: icAddQueue, do: func() { u.p.AddToQueue(ids) }},
		{label: "Add to Playlist", glyph: icPlaylist, sub: func() []menuItem { return u.playlistSubmenu(ids) }},
	}
	if first != nil {
		items = append(items, menuItem{separator: true}, menuItem{label: "Show in File Explorer", glyph: icExplorer,
			do: func() {
				if u.host.ShowInExplorer != nil {
					u.host.ShowInExplorer(first.Path)
				}
			}})
	}
	u.openMenu(at, "", items)
}

// ---- artist and folder lists ----

type listState struct {
	list widget.List
	rows map[string]*listRow
}

type listRow struct {
	click widget.Clickable
	tint  fade
}

type listEntry struct {
	key, title, sub, right string
	unknown                bool
	artPath, artKey        string
	artHas                 bool
	round                  bool   // artist avatar
	glyph                  string // folder icon instead of art
	open                   route
	ids                    []string
	first                  *library.Track
}

func (u *UI) layoutEntries(gtx layout.Context, r image.Rectangle, key string, entries []listEntry) {
	ls := u.lists[key]
	if ls == nil {
		ls = &listState{rows: map[string]*listRow{}}
		ls.list.Axis = layout.Vertical
		u.lists[key] = ls
	}
	rowH := dp(gtx, 56)
	defer u.push(gtx, r.Min)()
	defer clip.Rect(image.Rectangle{Max: r.Size()}).Push(gtx.Ops).Pop()
	gg := gtx
	gg.Constraints = layout.Exact(r.Size())
	org := u.org
	w := r.Dx()
	ls.list.Layout(gg, len(entries), func(gtx layout.Context, i int) layout.Dimensions {
		e := entries[i]
		lr := ls.rows[e.key]
		if lr == nil {
			lr = &listRow{}
			ls.rows[e.key] = lr
		}
		y := (i-ls.list.Position.First)*rowH - ls.list.Position.Offset
		u.org = org.Add(image.Pt(0, y))
		defer func() { u.org = org }()
		if lr.click.Clicked(gtx) {
			to := e.open
			u.later(func() { u.navigate(to) })
		}
		for {
			ev, ok := gtx.Event(pointer.Filter{Target: lr, Kinds: pointer.Press})
			if !ok {
				break
			}
			if pe, ok := ev.(pointer.Event); ok && pe.Buttons.Contain(pointer.ButtonSecondary) {
				u.openCollectionMenu(e.title, e.open.ref(), e.ids, e.first, u.mouse.pos)
			}
		}
		gg := gtx
		gg.Constraints = layout.Exact(image.Pt(w, rowH))
		lr.click.Layout(gg, func(gtx layout.Context) layout.Dimensions {
			inset := dp(gtx, 8)
			hv := lr.tint.to(boolf(lr.click.Hovered()), fast)
			bg := mixColor(pal.bg, pal.hover, hv)
			fillRRect(gtx.Ops, image.Rect(inset, dp(gtx, 2), w-inset, rowH-dp(gtx, 2)), dp(gtx, rSmall), withAlpha(pal.hover, hv))
			x := dp(gtx, 16)
			a := dp(gtx, 40)
			ar := image.Rect(x, (rowH-a)/2, x+a, (rowH+a)/2)
			switch {
			case e.glyph != "":
				fillRRect(gtx.Ops, ar, dp(gtx, rSmall), skeleton())
				c, d := u.icon(gtx, e.glyph, 20, pal.text2)
				place(gtx.Ops, image.Pt(ar.Min.X+(a-d.Size.X)/2, ar.Min.Y+(a-d.Size.Y)/2), c)
			case e.round:
				u.artwork(gtx, u.coverFor(e.artPath, e.artHas, a), e.artKey, ar, a/2)
			default:
				u.artwork(gtx, u.coverFor(e.artPath, e.artHas, a), e.artKey, ar, dp(gtx, rSmall))
			}
			tx := ar.Max.X + dp(gtx, 12)
			rightW := 0
			if e.right != "" {
				c, d := u.text(gtx, e.right, textStyle{size: fsCell, color: pal.text2})
				rightW = d.Size.X + dp(gtx, 16)
				place(gtx.Ops, image.Pt(w-dp(gtx, 16)-d.Size.X, (rowH-d.Size.Y)/2), c)
			}
			tw := w - tx - dp(gtx, 16) - rightW
			col := pal.text1
			if e.unknown {
				col = pal.text3
			}
			lh := dp(gtx, 20)
			top := (rowH - 2*lh) / 2
			if u.cell(gtx, e.title, textStyle{size: fsBody, weight: font.Medium, color: col}, image.Pt(tx, top), tw, lh, bg) &&
				lr.click.Hovered() {
				u.tipAtMouse(lr, e.title)
			}
			u.cell(gtx, e.sub, textStyle{size: fsCell, color: pal.text2}, image.Pt(tx, top+lh), tw, lh, bg)
			pointerCursor(gtx, image.Rect(0, 0, w, rowH), pointer.CursorPointer)
			return layout.Dimensions{Size: image.Pt(w, rowH)}
		})
		func() {
			defer clip.Rect(image.Rect(0, 0, w, rowH)).Push(gtx.Ops).Pop()
			defer pointer.PassOp{}.Push(gtx.Ops).Pop()
			event.Op(gtx.Ops, lr)
		}()
		return layout.Dimensions{Size: image.Pt(w, rowH)}
	})
}

func (u *UI) layoutArtistList(gtx layout.Context, r image.Rectangle, artists []*library.Artist) {
	entries := make([]listEntry, len(artists))
	for i, ar := range artists {
		t := ar.Tracks[0]
		for _, tr := range ar.Tracks {
			if tr.HasCover {
				t = tr
				break
			}
		}
		entries[i] = listEntry{key: ar.Key, title: orUnknown(ar.Name, "Unknown Artist"), unknown: ar.Name == "",
			sub:     countLabel(ar.Albums, "album") + " · " + countLabel(len(ar.Tracks), "track"),
			artPath: t.Path, artHas: t.HasCover, artKey: ar.Key, round: true,
			open: route{kind: routeArtist, key: ar.Key}, ids: library.IDs(ar.Tracks), first: ar.Tracks[0]}
	}
	u.layoutEntries(gtx, r, "artists", entries)
}

func (u *UI) layoutFolderList(gtx layout.Context, r image.Rectangle, folders []*library.Folder) {
	entries := make([]listEntry, len(folders))
	for i, fo := range folders {
		entries[i] = listEntry{key: fo.Path, title: filepath.Base(fo.Path), sub: filepath.Dir(fo.Path),
			right: countLabel(len(fo.Tracks), "track"), glyph: icFolder,
			open: route{kind: routeFolder, key: fo.Path}, ids: library.IDs(fo.Tracks), first: fo.Tracks[0]}
	}
	u.layoutEntries(gtx, r, "folders", entries)
}
