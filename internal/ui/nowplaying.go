package ui

import (
	"image"
	"image/color"
	"strconv"

	"gioui.org/font"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/paint"
	"gioui.org/text"

	"musicplayer/internal/player"
)

// nowPlayingState: the cover, the track's details and (when it has any) its synchronised lyrics.
//
//	no lyrics, wide  : [ cover | title/artist/chips ]        (centred)
//	no lyrics, narrow: cover above the text                   (centred)
//	lyrics,  wide    : [ cover + text (left) | lyrics ]
//	lyrics,  narrow  : compact [cover | text] row, lyrics below
type nowPlayingState struct {
	serial int
	enter  tween
}

func (u *UI) layoutNowPlaying(gtx layout.Context, area image.Rectangle) {
	s := &u.np
	if !u.p.HasTrack() {
		ic, id := u.icon(gtx, icNowPlaying, 64, pal.text3)
		tc, td := u.text(gtx, "Nothing playing", textStyle{size: 20, weight: font.SemiBold, color: pal.text1})
		hc, hd := u.text(gtx, "Double-click a song to play it", textStyle{size: fsBody, color: pal.text2})
		y := area.Min.Y + (area.Dy()-id.Size.Y-td.Size.Y-hd.Size.Y-dp(gtx, 24))/2
		cx := area.Min.X + area.Dx()/2
		place(gtx.Ops, image.Pt(cx-id.Size.X/2, y), ic)
		y += id.Size.Y + dp(gtx, 16)
		place(gtx.Ops, image.Pt(cx-td.Size.X/2, y), tc)
		place(gtx.Ops, image.Pt(cx-hd.Size.X/2, y+td.Size.Y+dp(gtx, 8)), hc)
		return
	}
	if serial := u.p.TrackSerial(); serial != s.serial {
		s.serial = serial
		s.enter.restart(260e6)
	}
	defer paint.PushOpacity(gtx.Ops, easeOutCubic(s.enter.progress())).Pop()

	usable := area.Size()
	lyricsOn := u.p.HasLyrics()
	sideBySide := lyricsOn && usable.X >= dp(gtx, 640)
	compact := lyricsOn && !sideBySide
	wideRow := !lyricsOn && usable.X > dp(gtx, 760)
	content := area
	switch {
	case sideBySide:
		infoW := max(dp(gtx, 260), int(float32(content.Dx())*0.42))
		info := image.Rect(content.Min.X, content.Min.Y, content.Min.X+infoW, content.Max.Y)
		coverSize := max(dp(gtx, 140), min(info.Dx(), int(float32(info.Dy())*0.55), dp(gtx, 360)))
		u.trackInfo(gtx, info, infoLayout{coverSize: coverSize})
		u.layoutLyrics(gtx, image.Rect(info.Max.X+dp(gtx, 40), content.Min.Y, content.Max.X, content.Max.Y))
	case compact:
		info := image.Rect(content.Min.X, content.Min.Y, content.Max.X, content.Min.Y+dp(gtx, 150))
		u.trackInfo(gtx, info, infoLayout{coverSize: dp(gtx, 120), columns: true, compact: true})
		u.layoutLyrics(gtx, image.Rect(content.Min.X, info.Max.Y+dp(gtx, 12), content.Max.X, content.Max.Y))
	case wideRow:
		coverSize := max(dp(gtx, 160), min(int(float32(usable.X)*0.42), int(float32(usable.Y)*0.72), dp(gtx, 560)))
		textMax := max(dp(gtx, 360), min(dp(gtx, 640), int(float32(usable.X)*0.36)))
		blockW := min(content.Dx(), coverSize+dp(gtx, 48)+textMax)
		x := content.Min.X + (content.Dx()-blockW)/2
		u.trackInfo(gtx, image.Rect(x, content.Min.Y, x+blockW, content.Max.Y), infoLayout{coverSize: coverSize, columns: true, wide: true})
	default:
		coverSize := max(dp(gtx, 160), min(int(float32(usable.X)*0.62), int(float32(usable.Y)*0.5), dp(gtx, 560)))
		u.trackInfo(gtx, content, infoLayout{coverSize: coverSize})
	}
}

type infoLayout struct {
	coverSize int
	columns   bool // cover | text in one row (text left-aligned); otherwise cover above centred text
	compact   bool
	wide      bool
}

func (u *UI) trackInfo(gtx layout.Context, r image.Rectangle, l infoLayout) {
	t := u.p.Current()
	if t == nil {
		return
	}
	info := t.Info
	cs := l.coverSize
	colGap := dp(gtx, 48)
	if l.compact {
		colGap = dp(gtx, 20)
	}
	textW := r.Dx()
	if l.columns {
		textW = r.Dx() - cs - colGap
	}
	textW = max(textW, dp(gtx, 80))
	titleSize, artistSize, albumSize := float32(fsPageHeader), float32(18), float32(fsBody)
	titleLines := 2
	switch {
	case l.wide:
		titleSize = 36
	case l.compact:
		titleSize, artistSize, albumSize, titleLines = 22, 15, fsCell, 1
	}
	align := text.Middle
	if l.columns {
		align = text.Start
	}
	tg := gtx
	tg.Constraints = layout.Constraints{Min: image.Pt(textW, 0), Max: image.Pt(textW, r.Dy())}
	type part struct {
		c op.CallOp
		d layout.Dimensions
	}
	var parts []part
	add := func(s string, st textStyle) {
		if s == "" {
			return
		}
		st.align = align
		c, d := record(tg, func(gtx layout.Context) layout.Dimensions { return u.label(gtx, s, st) })
		parts = append(parts, part{c, d})
	}
	add(info.Title, textStyle{size: titleSize, weight: font.Bold, color: pal.text1, maxLines: titleLines})
	add(orUnknown(info.Artist, "Unknown Artist"), textStyle{size: artistSize, weight: font.SemiBold, color: pal.text1})
	add(info.Album, textStyle{size: albumSize, color: pal.text2})
	chipsC, chipsD := record(tg, func(gtx layout.Context) layout.Dimensions { return u.chips(gtx, !l.columns) })
	textH := 0
	for i, p := range parts {
		if i > 0 {
			textH += dp(gtx, 6)
		}
		textH += p.d.Size.Y
	}
	textH += dp(gtx, 16) + chipsD.Size.Y
	var coverPos, textPos image.Point
	if l.columns {
		blockH := max(cs, textH)
		y0 := r.Min.Y + (r.Dy()-blockH)/2
		coverPos = image.Pt(r.Min.X, y0+(blockH-cs)/2)
		textPos = image.Pt(r.Min.X+cs+colGap, y0+(blockH-textH)/2)
	} else {
		blockH := cs + dp(gtx, 24) + textH
		y0 := r.Min.Y + (r.Dy()-blockH)/2
		coverPos = image.Pt(r.Min.X+(r.Dx()-cs)/2, y0)
		textPos = image.Pt(r.Min.X, y0+cs+dp(gtx, 24))
	}
	cr := image.Rectangle{Min: coverPos, Max: coverPos.Add(image.Pt(cs, cs))}
	shadow(gtx.Ops, cr, dp(gtx, rArt), dp(gtx, 32), dp(gtx, 12))
	u.artwork(gtx, u.p.Cover("large"), albumKeyOf(t), cr, dp(gtx, rArt))
	y := textPos.Y
	for i, p := range parts {
		if i > 0 {
			y += dp(gtx, 6)
		}
		place(gtx.Ops, image.Pt(textPos.X, y), p.c)
		y += p.d.Size.Y
	}
	place(gtx.Ops, image.Pt(textPos.X, y+dp(gtx, 16)), chipsC)
}

// chips is the wrapping row of the format badge and track number, year, genre, sample rate and bitrate.
func (u *UI) chips(gtx layout.Context, centred bool) layout.Dimensions {
	info := u.p.Current().Info
	var labels []string
	if info.TrackNumber > 0 {
		labels = append(labels, "Track "+strconv.Itoa(info.TrackNumber))
	}
	if info.Year > 0 {
		labels = append(labels, strconv.Itoa(info.Year))
	}
	if info.Genre != "" {
		labels = append(labels, info.Genre)
	}
	if info.SampleRate > 0 {
		labels = append(labels, player.KHz(info.SampleRate))
	}
	if info.Bitrate > 0 {
		labels = append(labels, strconv.Itoa(info.Bitrate)+" kbps")
	}
	h := dp(gtx, 26)
	var items []op.CallOp
	var widths []int
	chip := func(label string, fg, bg color.NRGBA, weight font.Weight) {
		lc, ld := u.text(gtx, label, textStyle{size: fsCaption, weight: weight, color: fg})
		w := ld.Size.X + dp(gtx, 20)
		c, _ := record(gtx, func(gtx layout.Context) layout.Dimensions {
			fillRRect(gtx.Ops, image.Rect(0, 0, w, h), dp(gtx, rSmall), bg)
			place(gtx.Ops, image.Pt(dp(gtx, 10), (h-ld.Size.Y)/2), lc)
			return layout.Dimensions{Size: image.Pt(w, h)}
		})
		items, widths = append(items, c), append(widths, w)
	}
	if info.Format != "" {
		fg, bg := pal.text2, pal.hover
		if info.Lossless {
			fg, bg = pal.accent, withAlpha(pal.accent, 0.16)
		}
		chip(info.Format, fg, bg, font.Bold)
	}
	for _, l := range labels {
		chip(l, pal.text2, pal.hover, font.Medium)
	}
	return flow(gtx, items, widths, h, dp(gtx, 8), centred)
}

// flow places fixed-height items left to right, wrapping at the available width.
func flow(gtx layout.Context, items []op.CallOp, widths []int, h, spacing int, centred bool) layout.Dimensions {
	maxW := gtx.Constraints.Max.X
	type line struct{ from, to, w int }
	var lines []line
	cur := line{}
	for i, w := range widths {
		if cur.to > cur.from && cur.w+spacing+w > maxW {
			lines = append(lines, cur)
			cur = line{from: i, to: i}
		}
		if cur.to > cur.from {
			cur.w += spacing
		}
		cur.w += w
		cur.to = i + 1
	}
	if cur.to > cur.from {
		lines = append(lines, cur)
	}
	y := 0
	for li, l := range lines {
		if li > 0 {
			y += spacing
		}
		x := 0
		if centred {
			x = max(0, (maxW-l.w)/2)
		}
		for i := l.from; i < l.to; i++ {
			place(gtx.Ops, image.Pt(x, y), items[i])
			x += widths[i] + spacing
		}
		y += h
	}
	return layout.Dimensions{Size: image.Pt(maxW, y)}
}
