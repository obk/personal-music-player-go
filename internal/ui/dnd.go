package ui

import (
	"image"

	"gioui.org/font"
	"gioui.org/layout"

	"musicplayer/internal/library"
)

type dropKind int

const (
	dropNone     dropKind = iota
	dropPlaylist          // onto a sidebar playlist: append
	dropUpNext            // into the queue drawer's Up Next at index
	dropInList            // into the playlist shown in the main view at index
)

type dropTarget struct {
	kind     dropKind
	playlist string
	index    int
}

type dragSourceKind int

const (
	fromTable dragSourceKind = iota
	fromUpNext
)

// dragState is an in-app drag of tracks: from a track list or the queue's Up Next, onto a playlist, Up Next, or
// (reordering) the playlist being shown. Drop targets claim the pointer while the frame is laid out; the drop
// applies the target claimed in the last frame.
type dragState struct {
	active   bool
	ids      []string
	label    string
	artPath  string
	artKey   string
	hasArt   bool
	source   dragSourceKind
	playlist string // source playlist when reordering one
	indexes  []int  // source positions (playlist or Up Next)
	pos      image.Point
	target   dropTarget
	last     dropTarget
}

func (d *dragState) beginFrame() {
	d.last = d.target
	d.target = dropTarget{}
}

// startDrag begins dragging tracks.
func (u *UI) startDrag(ids []string, source dragSourceKind, playlist string, indexes []int) {
	if len(ids) == 0 {
		return
	}
	d := &u.dnd
	*d = dragState{active: true, ids: ids, source: source, playlist: playlist, indexes: indexes, pos: u.mouse.pos}
	if t := u.lib.Track(ids[0]); t != nil {
		d.artPath, d.hasArt, d.artKey = t.Path, t.HasCover, t.Info.Album+"\x00"+t.ArtistName()
		d.label = t.Info.Title
	}
	if len(ids) > 1 {
		d.label = countLabel(len(ids), "track")
	}
	u.tooltip.key = nil
}

// finishDrag drops onto the last claimed target.
func (u *UI) finishDrag() {
	d := u.dnd
	u.dnd = dragState{}
	t := d.last
	if d.target.kind != dropNone {
		t = d.target
	}
	switch t.kind {
	case dropPlaylist:
		u.lib.AddToPlaylist(t.playlist, d.ids, -1)
		u.p.OnToast(toastf("Added %s to %s", describeIDs(u.lib, d.ids), t.playlist))
	case dropUpNext:
		if d.source == fromUpNext {
			u.p.Queue().MoveUpNext(d.indexes, t.index-countBefore(d.indexes, t.index))
		} else {
			u.p.Queue().InsertUpNext(t.index, d.ids)
		}
	case dropInList:
		if d.source == fromTable && d.playlist == t.playlist {
			u.lib.MoveInPlaylist(t.playlist, d.indexes, t.index-countBefore(d.indexes, t.index))
		} else {
			u.lib.AddToPlaylist(t.playlist, d.ids, t.index)
		}
	}
}

// countBefore is how many of indexes lie before position at (to convert an insertion point in the full list to
// one in the list without the moved items).
func countBefore(indexes []int, at int) int {
	n := 0
	for _, i := range indexes {
		if i < at {
			n++
		}
	}
	return n
}

func describeIDs(lib *library.Library, ids []string) string {
	if len(ids) == 1 {
		if t := lib.Track(ids[0]); t != nil {
			return "“" + t.Info.Title + "”"
		}
	}
	return countLabel(len(ids), "track")
}

// layoutDragGhost draws the dragged pill next to the pointer.
func (u *UI) layoutDragGhost(gtx layout.Context) {
	d := &u.dnd
	if !d.active {
		return
	}
	d.pos = u.mouse.pos
	h := dp(gtx, 40)
	art := dp(gtx, 24)
	c, td := u.text(gtx, d.label, textStyle{size: fsCell, weight: font.SemiBold, color: pal.text1})
	w := min(dp(gtx, 8)+art+dp(gtx, 10)+td.Size.X+dp(gtx, 14), dp(gtx, 280))
	r := image.Rect(0, 0, w, h).Add(d.pos.Add(image.Pt(dp(gtx, 14), dp(gtx, 10))))
	defer paintOpacity(gtx, 0.95).Pop()
	shadow(gtx.Ops, r, dp(gtx, rPanel), dp(gtx, 16), dp(gtx, 6))
	fillRRect(gtx.Ops, r, dp(gtx, rPanel), pal.elevated)
	ar := image.Rect(r.Min.X+dp(gtx, 8), r.Min.Y+(h-art)/2, r.Min.X+dp(gtx, 8)+art, r.Min.Y+(h+art)/2)
	u.artwork(gtx, u.coverFor(d.artPath, d.hasArt, art), d.artKey, ar, dp(gtx, rSmall))
	tx := ar.Max.X + dp(gtx, 10)
	u.cell(gtx, d.label, textStyle{size: fsCell, weight: font.SemiBold, color: pal.text1}, image.Pt(tx, r.Min.Y),
		r.Max.X-dp(gtx, 14)-tx, h, pal.elevated)
	_ = c
	clk.animating = true
}
