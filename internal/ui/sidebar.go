package ui

import (
	"image"
	"strings"

	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/unit"
	"gioui.org/widget"

	"musicplayer/internal/library"
)

type navItem struct {
	click widget.Clickable
	tint  fade
	right bool // right-clicked this frame (via raw pointer events)
}

type sidebarState struct {
	nav         [5]navItem
	liked       navItem
	playlists   map[string]*navItem
	newPlaylist iconButton
	addFolder   button
	addFolderIc iconButton
	theme       iconButton
	list        widget.List
}

type searchState struct {
	editor   widget.Editor
	last     string
	railOpen bool
	clearBtn iconButton
	railBtn  iconButton
	focused  fade
}

func (s *searchState) query() string { return strings.TrimSpace(s.editor.Text()) }

func (s *searchState) clear(u *UI) {
	s.editor.SetText("")
	s.last = ""
	if u.route.kind == routeSearch {
		u.goBack()
	}
}

var navRoutes = [5]struct {
	kind  routeKind
	label string
	glyph string
}{
	{routeNowPlaying, "Now Playing", icNowPlaying},
	{routeSongs, "Songs", icSongs},
	{routeAlbums, "Albums", icAlbum},
	{routeArtists, "Artists", icArtist},
	{routeFolders, "Folders", icFolder},
}

func (u *UI) layoutSidebar(gtx layout.Context, g geometry) {
	s := &u.sidebar
	r := g.sidebar
	fillRect(gtx.Ops, r, pal.surface)
	fillRect(gtx.Ops, image.Rect(r.Max.X-1, r.Min.Y, r.Max.X, r.Max.Y), pal.divider)
	moveArea(gtx, image.Rect(r.Min.X, 0, r.Max.X, dp(gtx, 14)))
	defer u.push(gtx, r.Min)()
	w := r.Dx()
	pad := dp(gtx, 16)
	y := pad

	// Search.
	if g.rail {
		func() {
			defer u.push(gtx, image.Pt((w-dp(gtx, 40))/2, y))()
			st := iconBtn(icSearch, 20)
			st.size = 40
			if _, clicked := u.search.railBtn.layout(u, gtx, st); clicked {
				u.search.railOpen = true
				gtx.Execute(key.FocusCmd{Tag: &u.search.editor})
			}
		}()
		if u.search.railOpen || u.search.query() != "" {
			// Expands in place, over the main view.
			sr := image.Rect(r.Min.X+dp(gtx, 8), y, r.Min.X+dp(gtx, 248), y+dp(gtx, 36))
			func() {
				defer u.push(gtx, r.Min.Mul(-1))()
				shadow(gtx.Ops, sr, dp(gtx, rSmall), dp(gtx, 16), dp(gtx, 4))
				fillRRect(gtx.Ops, sr, dp(gtx, rSmall), pal.elevated)
				u.searchField(gtx, sr)
			}()
			if !gtx.Focused(&u.search.editor) && u.search.query() == "" {
				u.search.railOpen = false
			}
		}
		y += dp(gtx, 40) + dp(gtx, 12)
	} else {
		u.searchField(gtx, image.Rect(pad, y, w-pad, y+dp(gtx, 36)))
		y += dp(gtx, 36) + pad
	}

	// Navigation.
	playingFrom := u.p.Queue().Context().Ref
	for i, n := range navRoutes {
		rt := route{kind: n.kind}
		active := u.route.kind == n.kind || (n.kind == routeAlbums && u.route.kind == routeAlbum) ||
			(n.kind == routeArtists && u.route.kind == routeArtist) || (n.kind == routeFolders && u.route.kind == routeFolder)
		playing := n.kind == routeSongs && playingFrom == "songs"
		if u.navRow(gtx, &s.nav[i], image.Rect(0, y, w, y+dp(gtx, 36)), n.label, n.glyph, active, playing, g.rail) {
			u.navigate(rt)
		}
		y += dp(gtx, 36) + dp(gtx, 2)
	}

	// Playlists.
	y += dp(gtx, 16)
	bottomH := dp(gtx, 60)
	if !g.rail {
		c, d := u.text(gtx, "PLAYLISTS", textStyle{size: fsColumn, weight: font.SemiBold, color: pal.text3})
		place(gtx.Ops, image.Pt(pad+dp(gtx, 4), y+(dp(gtx, 28)-d.Size.Y)/2), c)
		func() {
			defer u.push(gtx, image.Pt(w-pad-dp(gtx, 28), y))()
			st := iconBtn(icAdd, 20)
			st.size = 28
			if _, clicked := s.newPlaylist.layout(u, gtx, st); clicked {
				u.openNameDialog("New playlist", "Create", "", func(name string) {
					name = u.lib.NewPlaylist(name, nil)
					u.navigate(route{kind: routePlaylist, key: name})
				})
			}
			u.tipButton(gtx, &s.newPlaylist, "New playlist", image.Rect(0, 0, dp(gtx, 28), dp(gtx, 28)))
		}()
		y += dp(gtx, 28) + dp(gtx, 4)
	} else {
		fillRect(gtx.Ops, image.Rect(dp(gtx, 16), y-dp(gtx, 8), w-dp(gtx, 16), y-dp(gtx, 7)), pal.divider)
	}
	listR := image.Rect(0, y, w, r.Dy()-bottomH)
	u.playlistNav(gtx, listR, g.rail, playingFrom)

	// Bottom: add music, theme.
	by := r.Dy() - bottomH
	fillRect(gtx.Ops, image.Rect(0, by, w, by+1), pal.divider)
	bh := dp(gtx, 36)
	themeGlyph := map[string]string{"": icAuto, "light": icLight, "dark": icDark}[u.themeSetting()]
	themeTip := map[string]string{"": "Theme: follow Windows", "light": "Theme: light", "dark": "Theme: dark"}[u.themeSetting()]
	if g.rail {
		func() {
			defer u.push(gtx, image.Pt((w-dp(gtx, 40))/2, by+dp(gtx, 10)))()
			st := iconBtn(icAddFolder, 20)
			st.size = 40
			if _, clicked := s.addFolderIc.layout(u, gtx, st); clicked && u.host.AddFolder != nil {
				u.host.AddFolder()
			}
		}()
		return
	}
	func() {
		defer u.push(gtx, image.Pt(pad-dp(gtx, 4), by+(bottomH-bh)/2))()
		gg := gtx
		if _, clicked := s.addFolder.layout(u, gg, "Add folder", icAddFolder, btnGhost, 36); clicked && u.host.AddFolder != nil {
			u.host.AddFolder()
		}
	}()
	func() {
		defer u.push(gtx, image.Pt(w-pad-bh, by+(bottomH-bh)/2))()
		st := iconBtn(themeGlyph, 20)
		st.size = 36
		if _, clicked := s.theme.layout(u, gtx, st); clicked {
			u.cycleTheme()
		}
		u.tipButton(gtx, &s.theme, themeTip, image.Rect(0, 0, bh, bh))
	}()
}

// tipButton shows a tooltip for an icon button while it is hovered (r in the current coordinates).
func (u *UI) tipButton(gtx layout.Context, b *iconButton, text string, r image.Rectangle) {
	if b.click.Hovered() {
		u.tip(b, text, u.wr(r))
	}
}

func (u *UI) searchField(gtx layout.Context, r image.Rectangle) {
	s := &u.search
	for {
		ev, ok := s.editor.Update(gtx)
		if !ok {
			break
		}
		if _, ok := ev.(widget.SubmitEvent); ok {
			gtx.Execute(key.FocusCmd{})
		}
	}
	s.editor.SingleLine, s.editor.Submit = true, true
	if q := s.query(); q != s.last {
		s.last = q
		switch {
		case q != "" && u.route.kind != routeSearch:
			u.navigate(route{kind: routeSearch})
		case q == "" && u.route.kind == routeSearch:
			u.goBack()
		}
	}
	focused := gtx.Focused(&s.editor)
	bg := pal.hover
	if pal.light {
		bg = pal.bg
	}
	rad := dp(gtx, rSmall)
	fillRRect(gtx.Ops, r, rad, bg)
	if f := s.focused.to(boolf(focused), fast); f > 0 {
		strokeRRect(gtx.Ops, r, rad, float32(dp(gtx, 1)), withAlpha(pal.accent, f))
	} else if pal.light {
		strokeRRect(gtx.Ops, r, rad, 1, pal.divider)
	}
	ic, id := u.icon(gtx, icSearch, 18, pal.text3)
	place(gtx.Ops, image.Pt(r.Min.X+dp(gtx, 10), r.Min.Y+(r.Dy()-id.Size.Y)/2), ic)
	tx := r.Min.X + dp(gtx, 10) + id.Size.X + dp(gtx, 8)
	right := r.Max.X - dp(gtx, 8)
	if s.editor.Len() > 0 {
		right -= dp(gtx, 24)
		func() {
			defer u.push(gtx, image.Pt(right, r.Min.Y+(r.Dy()-dp(gtx, 24))/2))()
			st := iconBtn(icClose, 16)
			st.size = 24
			if _, clicked := s.clearBtn.layout(u, gtx, st); clicked {
				u.later(func() { s.clear(u) })
			}
		}()
	} else if !focused {
		c, d := u.text(gtx, "Ctrl+F", textStyle{size: fsColumn, weight: font.Medium, color: pal.text3})
		place(gtx.Ops, image.Pt(right-d.Size.X, r.Min.Y+(r.Dy()-d.Size.Y)/2), c)
		right -= d.Size.X + dp(gtx, 4)
		c, d = u.text(gtx, "Search", textStyle{size: fsCell, color: pal.text3})
		place(gtx.Ops, image.Pt(tx, r.Min.Y+(r.Dy()-d.Size.Y)/2), c)
	}
	u.at(gtx, image.Pt(tx, r.Min.Y), image.Pt(right-tx, r.Dy()), func(gtx layout.Context) layout.Dimensions {
		c, d := record(gtx, func(gtx layout.Context) layout.Dimensions {
			return s.editor.Layout(gtx, u.shaper, uiFont(font.Normal), unit.Sp(fsCell), colorMaterial(gtx.Ops, pal.text1),
				colorMaterial(gtx.Ops, withAlpha(pal.accent, 0.35)))
		})
		place(gtx.Ops, image.Pt(0, (r.Dy()-d.Size.Y)/2), c)
		pointerCursor(gtx, image.Rect(0, 0, right-tx, r.Dy()), pointer.CursorText)
		return layout.Dimensions{Size: image.Pt(right-tx, r.Dy())}
	})
}

// navRow draws one navigation entry and reports a click.
func (u *UI) navRow(gtx layout.Context, n *navItem, r image.Rectangle, label, glyph string, active, playing, rail bool) bool {
	clicked := n.click.Clicked(gtx)
	u.at(gtx, r.Min, r.Size(), func(gtx layout.Context) layout.Dimensions {
		w, h := r.Dx(), r.Dy()
		gtx.Constraints = layout.Exact(image.Pt(w, h))
		return n.click.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			inset := dp(gtx, 8)
			bg := image.Rect(inset, 0, w-inset, h)
			hv := n.tint.to(boolf(n.click.Hovered()), fast)
			switch {
			case active:
				fillRRect(gtx.Ops, bg, dp(gtx, rSmall), pal.selected)
				fillRRect(gtx.Ops, image.Rect(inset, dp(gtx, 6), inset+dp(gtx, 3), h-dp(gtx, 6)), dp(gtx, 2), pal.accent)
			case hv > 0:
				fillRRect(gtx.Ops, bg, dp(gtx, rSmall), withAlpha(pal.hover, hv))
			}
			col := pal.text2
			if active {
				col = pal.text1
			}
			ic, id := u.icon(gtx, glyph, 20, col)
			if rail {
				place(gtx.Ops, image.Pt((w-id.Size.X)/2, (h-id.Size.Y)/2), ic)
				if n.click.Hovered() {
					u.tip(n, label, u.wr(image.Rect(0, 0, w, h)))
				}
			} else {
				ix := inset + dp(gtx, 12)
				place(gtx.Ops, image.Pt(ix, (h-id.Size.Y)/2), ic)
				tx := ix + id.Size.X + dp(gtx, 12)
				right := w - inset - dp(gtx, 8)
				if playing {
					sc, sd := u.icon(gtx, icVolumeUp, 14, pal.accent)
					right -= sd.Size.X
					place(gtx.Ops, image.Pt(right, (h-sd.Size.Y)/2), sc)
					right -= dp(gtx, 6)
				}
				cellBg := mixColor(pal.surface, pal.hover, hv)
				if active {
					cellBg = pal.selected
				}
				if u.cell(gtx, label, textStyle{size: fsBody, weight: font.Medium, color: col}, image.Pt(tx, 0), right-tx, h, cellBg) &&
					n.click.Hovered() {
					u.tip(n, label, u.wr(image.Rect(tx, 0, right, h)))
				}
			}
			pointerCursor(gtx, image.Rect(0, 0, w, h), pointer.CursorPointer)
			return layout.Dimensions{Size: image.Pt(w, h)}
		})
	})
	return clicked
}

// playlistNav lists Liked Songs and the user's playlists; they accept dragged tracks and have a context menu.
func (u *UI) playlistNav(gtx layout.Context, r image.Rectangle, rail bool, playingFrom string) {
	s := &u.sidebar
	if s.playlists == nil {
		s.playlists = map[string]*navItem{}
	}
	lists := u.lib.Playlists()
	type entry struct {
		name  string
		liked bool
	}
	entries := []entry{{liked: true}}
	for _, pl := range lists {
		entries = append(entries, entry{name: pl.Name})
	}
	rowH := dp(gtx, 36) + dp(gtx, 2)
	s.list.Axis = layout.Vertical
	defer u.push(gtx, r.Min)()
	defer clip.Rect(image.Rectangle{Max: r.Size()}).Push(gtx.Ops).Pop()
	gtx.Constraints = layout.Exact(r.Size())
	org := u.org
	s.list.Layout(gtx, len(entries), func(gtx layout.Context, i int) layout.Dimensions {
		u.org = org.Add(image.Pt(0, (i-s.list.Position.First)*rowH-s.list.Position.Offset))
		defer func() { u.org = org }()
		e := entries[i]
		n := &s.liked
		label, glyph := "Liked Songs", icHeart
		rt := route{kind: routeLiked}
		ref := "liked"
		if !e.liked {
			n = s.playlists[e.name]
			if n == nil {
				n = &navItem{}
				s.playlists[e.name] = n
			}
			label, glyph = e.name, icPlaylist
			rt = route{kind: routePlaylist, key: e.name}
			ref = rt.ref()
		}
		row := image.Rect(0, 0, r.Dx(), dp(gtx, 36))
		// Right-click opens the playlist's menu.
		for {
			ev, ok := gtx.Event(pointer.Filter{Target: n, Kinds: pointer.Press})
			if !ok {
				break
			}
			if pe, ok := ev.(pointer.Event); ok && pe.Buttons.Contain(pointer.ButtonSecondary) {
				u.openPlaylistMenu(e.name, e.liked, u.mouse.pos)
			}
		}
		if u.navRow(gtx, n, row, label, glyph, u.route == rt, playingFrom == ref, rail) {
			u.navigate(rt)
		}
		func() {
			defer clip.Rect(row).Push(gtx.Ops).Pop()
			defer pointer.PassOp{}.Push(gtx.Ops).Pop()
			event.Op(gtx.Ops, n)
		}()
		// Drop target for dragged tracks.
		if !e.liked && u.dnd.active && u.dnd.ids != nil {
			win := u.wr(row)
			if u.dnd.pos.In(win) {
				strokeRRect(gtx.Ops, row.Inset(dp(gtx, 8)).Inset(-dp(gtx, 1)), dp(gtx, rSmall), float32(dp(gtx, 2)), pal.accent)
				name := e.name
				u.dnd.target = dropTarget{kind: dropPlaylist, playlist: name}
			}
		}
		return layout.Dimensions{Size: image.Pt(r.Dx(), rowH)}
	})
}

// openPlaylistMenu is the right-click menu of a sidebar playlist.
func (u *UI) openPlaylistMenu(name string, liked bool, at image.Point) {
	ids := func() []string {
		if liked {
			return library.IDs(u.lib.Liked())
		}
		if pl := u.lib.Playlist(name); pl != nil {
			return pl.IDs
		}
		return nil
	}
	ctxName, ref := "Liked Songs", "liked"
	if !liked {
		ctxName, ref = name, route{kind: routePlaylist, key: name}.ref()
	}
	items := []menuItem{
		{label: "Play", glyph: icPlay, disabled: len(ids()) == 0, do: func() { u.p.PlayContext(ctxName, ref, ids(), 0) }},
		{label: "Play Next", glyph: icPlayNext, disabled: len(ids()) == 0, do: func() { u.p.PlayNext(ids()) }},
		{label: "Add to Queue", glyph: icAddQueue, disabled: len(ids()) == 0, do: func() { u.p.AddToQueue(ids()) }},
	}
	if !liked {
		items = append(items, menuItem{separator: true},
			menuItem{label: "Rename…", glyph: icEdit, do: func() {
				u.openNameDialog("Rename playlist", "Rename", name, func(to string) {
					to = u.lib.RenamePlaylist(name, to)
					if u.route == (route{kind: routePlaylist, key: name}) {
						u.route.key = to
					}
				})
			}},
			menuItem{label: "Export as M3U…", glyph: icExport, do: func() {
				if u.host.ExportPlaylist != nil {
					u.host.ExportPlaylist(name)
				}
			}},
			menuItem{separator: true},
			menuItem{label: "Delete playlist", glyph: icDelete, danger: true, do: func() {
				u.openConfirm("Delete “"+name+"”?", "The tracks stay in your library.", "Delete", func() {
					u.lib.DeletePlaylist(name)
					if u.route == (route{kind: routePlaylist, key: name}) {
						u.route = route{kind: routeSongs}
					}
				})
			}},
		)
	}
	u.openMenu(at, "", items)
}
