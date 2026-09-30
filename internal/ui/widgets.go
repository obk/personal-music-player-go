package ui

import (
	"hash/fnv"
	"image"
	"image/color"
	"math"
	"time"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"

	"musicplayer/internal/metadata"
)

// ---- layout helpers ----

// record lays out w into a macro and returns it with its size.
func record(gtx layout.Context, w layout.Widget) (op.CallOp, layout.Dimensions) {
	m := op.Record(gtx.Ops)
	d := w(gtx)
	return m.Stop(), d
}

// place draws a recorded widget at pt.
func place(ops *op.Ops, pt image.Point, c op.CallOp) {
	defer op.Offset(pt).Push(ops).Pop()
	c.Add(ops)
}

// at runs w with its origin at pt and the given maximum size.
func at(gtx layout.Context, pt image.Point, size image.Point, w layout.Widget) layout.Dimensions {
	defer op.Offset(pt).Push(gtx.Ops).Pop()
	gtx.Constraints = layout.Constraints{Max: size}
	return w(gtx)
}

// scaleAbout pushes a uniform scale around a centre point.
func scaleAbout(ops *op.Ops, center image.Point, s float32) op.TransformStack {
	c := f32.Pt(float32(center.X), float32(center.Y))
	return op.Affine(f32.Affine2D{}.Scale(c, f32.Pt(s, s))).Push(ops)
}

func pointerCursor(gtx layout.Context, r image.Rectangle, c pointer.Cursor) {
	defer clip.Rect(r).Push(gtx.Ops).Pop()
	c.Add(gtx.Ops)
}

func paintOpacity(gtx layout.Context, a float32) paint.OpacityStack {
	return paint.PushOpacity(gtx.Ops, a)
}

func dp(gtx layout.Context, v float32) int { return gtx.Dp(unit.Dp(v)) }

// ---- text ----

type textStyle struct {
	size     float32
	weight   font.Weight
	color    color.NRGBA
	maxLines int // 0 = 1 line; -1 = unlimited
	align    text.Alignment
	icon     bool
	noTrunc  bool // no ellipsis (the caller fades or clips)
}

func (u *UI) label(gtx layout.Context, s string, st textStyle) layout.Dimensions {
	f := uiFont(st.weight)
	if st.icon {
		f = iconFont
	}
	lines := st.maxLines
	switch lines {
	case 0:
		lines = 1
	case -1:
		lines = 0
	}
	l := widget.Label{MaxLines: lines, Alignment: st.align, WrapPolicy: text.WrapWords}
	if !st.noTrunc {
		l.Truncator = "…"
	}
	return l.Layout(gtx, u.shaper, f, unit.Sp(st.size), s, colorMaterial(gtx.Ops, st.color))
}

// text records a label with unconstrained width and returns it with its size.
func (u *UI) text(gtx layout.Context, s string, st textStyle) (op.CallOp, layout.Dimensions) {
	return record(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min = image.Point{}
		return u.label(gtx, s, st)
	})
}

// icon records a Material Icons glyph.
func (u *UI) icon(gtx layout.Context, glyph string, size float32, c color.NRGBA) (op.CallOp, layout.Dimensions) {
	return u.text(gtx, glyph, textStyle{size: size, color: c, icon: true})
}

// cell draws one line of text at pt within width. Text that does not fit is cut and faded out over its last 32 dp
// into bg (no ellipsis); it then reports truncated, so the caller can offer the full text as a tooltip.
func (u *UI) cell(gtx layout.Context, s string, st textStyle, pt image.Point, width, height int, bg color.NRGBA) (truncated bool) {
	if width <= 0 || s == "" {
		return false
	}
	st.noTrunc = true
	gg := gtx
	gg.Constraints = layout.Constraints{Max: image.Pt(1<<20, height)}
	c, d := record(gg, func(gtx layout.Context) layout.Dimensions { return u.label(gtx, s, st) })
	y := pt.Y + (height-d.Size.Y)/2
	if d.Size.X <= width {
		place(gtx.Ops, image.Pt(pt.X, y), c)
		return false
	}
	func() {
		defer clip.Rect(image.Rect(pt.X, pt.Y, pt.X+width, pt.Y+height)).Push(gtx.Ops).Pop()
		place(gtx.Ops, image.Pt(pt.X, y), c)
	}()
	fw := min(dp(gtx, 32), width)
	clear := bg
	clear.A = 0
	hgradient(gtx.Ops, image.Rect(pt.X+width-fw, pt.Y, pt.X+width, pt.Y+height), clear, bg)
	return true
}

// ---- tooltips ----

// tooltipState shows the full text of a truncated field after the pointer rests on it for 600 ms.
type tooltipState struct {
	key    any
	text   string
	anchor image.Rectangle
	since  time.Time
	seen   bool // requested this frame
}

// tip asks for a tooltip for key (anything identifying the field) this frame; anchor is in window coordinates.
func (u *UI) tip(key any, text string, anchor image.Rectangle) {
	t := &u.tooltip
	if t.key != key || t.text != text {
		t.key, t.text, t.since = key, text, clk.now
	}
	t.anchor, t.seen = anchor, true
}

func (u *UI) layoutTooltip(gtx layout.Context) {
	t := &u.tooltip
	if !t.seen {
		t.key = nil
		return
	}
	t.seen = false
	if wait := 600*time.Millisecond - clk.now.Sub(t.since); wait > 0 {
		gtx.Execute(op.InvalidateCmd{At: clk.now.Add(wait)})
		return
	}
	maxW := dp(gtx, 480)
	pad := dp(gtx, 8)
	gg := gtx
	gg.Constraints = layout.Constraints{Max: image.Pt(maxW-2*pad, 1<<20)}
	c, d := record(gg, func(gtx layout.Context) layout.Dimensions {
		return u.label(gtx, t.text, textStyle{size: fsCaption, color: pal.text1, maxLines: -1})
	})
	w, h := d.Size.X+2*pad, d.Size.Y+2*dp(gtx, 6)
	ws := gtx.Constraints.Max
	x := max(dp(gtx, 4), min(t.anchor.Min.X, ws.X-w-dp(gtx, 4)))
	y := t.anchor.Max.Y + dp(gtx, 6)
	if y+h > ws.Y-dp(gtx, 4) {
		y = t.anchor.Min.Y - h - dp(gtx, 6)
	}
	r := image.Rect(x, y, x+w, y+h)
	shadow(gtx.Ops, r, dp(gtx, rPanel), dp(gtx, 16), dp(gtx, 4))
	fillRRect(gtx.Ops, r, dp(gtx, rPanel), pal.elevated)
	if pal.light {
		strokeRRect(gtx.Ops, r, dp(gtx, rPanel), 1, pal.divider)
	}
	place(gtx.Ops, image.Pt(x+pad, y+dp(gtx, 6)), c)
}

// ---- buttons ----

// iconButton is a square glyph button: quiet tint at rest, hover background (4 dp radius), accent when active.
// filled gives the round accent play button.
type iconButton struct {
	click widget.Clickable
	tint  fade
	press spring
}

type iconStyle struct {
	glyph    string
	iconSize float32
	size     float32 // dp; 0 = iconSize + 12
	color    color.NRGBA
	hoverCol color.NRGBA
	hoverBg  color.NRGBA
	active   bool
	filled   bool
	disabled bool
}

func iconBtn(glyph string, size float32) iconStyle {
	return iconStyle{glyph: glyph, iconSize: size, color: pal.text2, hoverCol: pal.text1, hoverBg: pal.hover}
}

// layout draws the button with its top-left at the current origin and reports a click.
func (b *iconButton) layout(u *UI, gtx layout.Context, st iconStyle) (layout.Dimensions, bool) {
	sz := st.size
	if sz == 0 {
		sz = st.iconSize + 12
	}
	px := dp(gtx, sz)
	if st.disabled {
		gtx = gtx.Disabled()
	}
	clicked := b.click.Clicked(gtx)
	hovered, pressed := b.click.Hovered() && !st.disabled, b.click.Pressed() && !st.disabled
	h := b.tint.to(boolf(hovered), fast)
	scale := b.press.to(1 - 0.06*boolf(pressed))
	gtx.Constraints = layout.Exact(image.Pt(px, px))
	dims := b.click.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		r := image.Rect(0, 0, px, px)
		opacity := float32(1)
		if st.disabled {
			opacity = 0.35
		}
		defer paint.PushOpacity(gtx.Ops, opacity).Pop()
		defer scaleAbout(gtx.Ops, r.Max.Div(2), scale).Pop()
		col := mixColor(st.color, st.hoverCol, h)
		switch {
		case st.filled:
			bg := mixColor(pal.accent, pal.accentHover, h)
			if pressed {
				bg = pal.accentPressed
			}
			fillCircle(gtx.Ops, r.Max.Div(2), px/2, bg)
			col = pal.onAccent
		case h > 0:
			fillRRect(gtx.Ops, r, dp(gtx, rSmall), withAlpha(st.hoverBg, h))
		}
		if st.active && !st.filled {
			col = pal.accent
		}
		c, d := u.icon(gtx, st.glyph, st.iconSize, col)
		place(gtx.Ops, image.Pt((px-d.Size.X)/2, (px-d.Size.Y)/2), c)
		if !st.disabled {
			pointerCursor(gtx, r, pointer.CursorPointer)
		}
		return layout.Dimensions{Size: r.Max}
	})
	return dims, clicked && !st.disabled
}

// button is a text button (optionally with a leading icon).
type button struct {
	click widget.Clickable
	tint  fade
}

type buttonKind int

const (
	btnPrimary   buttonKind = iota // accent fill
	btnSecondary                   // hover-colour fill
	btnGhost                       // transparent, hover fill
	btnDanger                      // danger fill
)

func (b *button) layout(u *UI, gtx layout.Context, label, glyph string, kind buttonKind, height float32) (layout.Dimensions, bool) {
	clicked := b.click.Clicked(gtx)
	h := dp(gtx, height)
	fg := pal.text1
	switch kind {
	case btnPrimary:
		fg = pal.onAccent
	case btnDanger:
		fg = argb(0xFFFFFFFF)
	}
	tc, td := u.text(gtx, label, textStyle{size: fsBody, weight: font.SemiBold, color: fg})
	var ic op.CallOp
	var id layout.Dimensions
	if glyph != "" {
		ic, id = u.icon(gtx, glyph, 18, fg)
	}
	pad := dp(gtx, 16)
	w := td.Size.X + 2*pad
	if glyph != "" {
		w += id.Size.X + dp(gtx, 6)
	}
	gtx.Constraints = layout.Exact(image.Pt(w, h))
	d := b.click.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		hv := b.tint.to(boolf(b.click.Hovered()), fast)
		r := image.Rect(0, 0, w, h)
		var bg color.NRGBA
		switch kind {
		case btnPrimary:
			bg = mixColor(pal.accent, pal.accentHover, hv)
			if b.click.Pressed() {
				bg = pal.accentPressed
			}
		case btnSecondary:
			bg = mixColor(pal.hover, pal.selected, hv)
		case btnGhost:
			bg = withAlpha(pal.hover, hv)
		case btnDanger:
			bg = mixColor(pal.danger, mixColor(pal.danger, argb(0xFFFFFFFF), 0.15), hv)
		}
		fillRRect(gtx.Ops, r, dp(gtx, rSmall), bg)
		x := pad
		if glyph != "" {
			place(gtx.Ops, image.Pt(x, (h-id.Size.Y)/2), ic)
			x += id.Size.X + dp(gtx, 6)
		}
		place(gtx.Ops, image.Pt(x, (h-td.Size.Y)/2), tc)
		pointerCursor(gtx, r, pointer.CursorPointer)
		return layout.Dimensions{Size: r.Max}
	})
	return d, clicked
}

// ---- slider (seek bar / volume) ----

// slider: a 4 dp track with an accent fill; on hover it grows to 6 dp and a 12 dp thumb appears. Clicking
// anywhere jumps there; dragging scrubs.
type slider struct {
	hovered bool
	pressed bool
	hoverX  float32
	value   float32 // 0..1 while pressed
	thick   fade
	thumb   fade
}

// layout fills the available width (24 dp tall). frac is the value shown when not pressed. It returns the user's
// value and whether it moved this frame.
func (s *slider) layout(gtx layout.Context, frac float32, enabled bool) (layout.Dimensions, float32, bool) {
	w := gtx.Constraints.Max.X
	h := dp(gtx, 24)
	moved := false
	for enabled {
		ev, ok := gtx.Event(pointer.Filter{Target: s,
			Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel | pointer.Enter | pointer.Leave | pointer.Move})
		if !ok {
			break
		}
		e, ok := ev.(pointer.Event)
		if !ok {
			continue
		}
		v := clamp01(e.Position.X / float32(max(1, w)))
		switch e.Kind {
		case pointer.Enter, pointer.Move:
			s.hovered, s.hoverX = true, e.Position.X
		case pointer.Leave:
			s.hovered = false
		case pointer.Press:
			if e.Buttons.Contain(pointer.ButtonPrimary) || e.Source == pointer.Touch {
				s.pressed, s.value, moved = true, v, true
			}
		case pointer.Drag:
			if s.pressed {
				s.value, moved, s.hoverX = v, true, e.Position.X
			}
		case pointer.Release:
			if s.pressed {
				s.value, moved = v, true
			}
			s.pressed = false
		case pointer.Cancel:
			s.pressed = false
		}
	}
	if !enabled {
		s.pressed, s.hovered = false, false
	}
	if s.pressed {
		frac = s.value
	}
	frac = clamp01(frac)
	active := s.hovered || s.pressed
	op := paint.PushOpacity(gtx.Ops, 1-0.65*boolf(!enabled))
	th := dp(gtx, 4+2*s.thick.to(boolf(active), fast))
	rail := image.Rect(0, (h-th)/2, w, (h-th)/2+th)
	railCol := pal.hover
	if pal.light {
		railCol = pal.divider
	}
	fillRRect(gtx.Ops, rail, th/2, railCol)
	fx := int(frac * float32(w))
	fillRRect(gtx.Ops, image.Rect(0, rail.Min.Y, fx, rail.Max.Y), th/2, pal.accent)
	if a := s.thumb.to(boolf(active), fast); a > 0.01 {
		r := int(float32(dp(gtx, 6)) * a)
		fillCircle(gtx.Ops, image.Pt(min(max(fx, r), w-r), h/2), r, pal.text1)
	}
	op.Pop()
	area := clip.Rect(image.Rect(0, 0, w, h)).Push(gtx.Ops)
	event.Op(gtx.Ops, s)
	if enabled {
		pointer.CursorPointer.Add(gtx.Ops)
	}
	area.Pop()
	return layout.Dimensions{Size: image.Pt(w, h)}, frac, moved
}

// ---- toggle switch ----

type toggle struct {
	click widget.Clickable
	knob  spring
	tint  fade
}

func (t *toggle) layout(gtx layout.Context, on bool) (layout.Dimensions, bool) {
	clicked := t.click.Clicked(gtx)
	w, h := dp(gtx, 38), dp(gtx, 22)
	gtx.Constraints = layout.Exact(image.Pt(w, h))
	d := t.click.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		off := pal.hover
		if pal.light {
			off = pal.divider
		}
		fillRRect(gtx.Ops, image.Rect(0, 0, w, h), h/2, mixColor(off, pal.accent, t.tint.to(boolf(on), fast)))
		k, m := dp(gtx, 16), dp(gtx, 3)
		kx := m + int(t.knob.to(boolf(on))*float32(w-k-2*m))
		knob := pal.text1
		if on {
			knob = pal.onAccent
		} else if pal.light {
			knob = argb(0xFFFFFFFF)
		}
		fillCircle(gtx.Ops, image.Pt(kx+k/2, h/2), k/2, knob)
		pointerCursor(gtx, image.Rect(0, 0, w, h), pointer.CursorPointer)
		return layout.Dimensions{Size: image.Pt(w, h)}
	})
	return d, clicked
}

// ---- chip (equalizer presets) ----

type chip struct {
	click  widget.Clickable
	remove widget.Clickable
	tint   fade
}

// layout draws a small button; removable chips show an × on hover. It returns (clicked, removeClicked).
func (c *chip) layout(u *UI, gtx layout.Context, label string, selected, removable bool) (layout.Dimensions, bool, bool) {
	clicked := c.click.Clicked(gtx)
	removed := c.remove.Clicked(gtx)
	showX := removable && (c.click.Hovered() || c.remove.Hovered())
	h := dp(gtx, 28)
	col := pal.text2
	if selected {
		col = pal.text1
	}
	lc, ld := u.text(gtx, label, textStyle{size: fsCaption, weight: font.Medium, color: col})
	xw := 0
	if showX {
		xw = dp(gtx, 18)
	}
	w := ld.Size.X + dp(gtx, 20) + xw
	gg := gtx
	gg.Constraints = layout.Exact(image.Pt(w, h))
	d := c.click.Layout(gg, func(gtx layout.Context) layout.Dimensions {
		r := image.Rect(0, 0, w, h)
		bg := mixColor(pal.hover, pal.selected, c.tint.to(boolf(c.click.Hovered()), fast))
		if selected {
			bg = withAlpha(pal.accent, 0.2)
		}
		fillRRect(gtx.Ops, r, dp(gtx, rSmall), bg)
		if selected {
			strokeRRect(gtx.Ops, r, dp(gtx, rSmall), float32(dp(gtx, 1)), pal.accent)
		}
		place(gtx.Ops, image.Pt(dp(gtx, 10), (h-ld.Size.Y)/2), lc)
		pointerCursor(gtx, r, pointer.CursorPointer)
		return layout.Dimensions{Size: r.Max}
	})
	if showX {
		x0 := dp(gtx, 10) + ld.Size.X + dp(gtx, 2)
		at(gtx, image.Pt(x0, 0), image.Pt(xw, h), func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints = layout.Exact(image.Pt(xw, h))
			return c.remove.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				col := pal.text3
				if c.remove.Hovered() {
					col = pal.danger
				}
				ic, id := u.icon(gtx, icClose, 14, col)
				place(gtx.Ops, image.Pt((xw-id.Size.X)/2, (h-id.Size.Y)/2), ic)
				return layout.Dimensions{Size: image.Pt(xw, h)}
			})
		})
	}
	return d, clicked, removed
}

// ---- artwork ----

// artGradient picks the placeholder colours of an album deterministically.
func artGradient(key string) [2]color.NRGBA {
	h := fnv.New32a()
	h.Write([]byte(key))
	return pal.artPairs[h.Sum32()%uint32(len(pal.artPairs))]
}

// artwork draws a cover into r (cropped to fill), or the album's generated placeholder: a two-tone diagonal
// gradient with a note glyph at 40% of the tile.
func (u *UI) artwork(gtx layout.Context, img *image.RGBA, key string, r image.Rectangle, radius int) {
	if img != nil {
		u.drawImageCover(gtx.Ops, img, r, radius, 1)
		return
	}
	g := artGradient(key)
	func() {
		defer clip.UniformRRect(r, radius).Push(gtx.Ops).Pop()
		paint.LinearGradientOp{Stop1: f32.Pt(float32(r.Min.X), float32(r.Min.Y)), Color1: g[0],
			Stop2: f32.Pt(float32(r.Max.X), float32(r.Max.Y)), Color2: g[1]}.Add(gtx.Ops)
		paint.PaintOp{}.Add(gtx.Ops)
	}()
	size := float32(min(r.Dx(), r.Dy())) * 0.4 / gtx.Metric.PxPerSp
	c, d := u.icon(gtx, icNote, size, pal.artIcon)
	place(gtx.Ops, image.Pt(r.Min.X+(r.Dx()-d.Size.X)/2, r.Min.Y+(r.Dy()-d.Size.Y)/2), c)
}

// coverFor returns the cached thumbnail of a track (requesting it when missing), or nil.
func (u *UI) coverFor(path string, hasCover bool, px int) *image.RGBA {
	if px > int(metadata.Small) {
		if img := u.p.Covers().Find(path, metadata.Medium); img != nil {
			return img
		}
	} else {
		if img := u.p.Covers().Find(path, metadata.Small); img != nil {
			return img
		}
		if img := u.p.Covers().Find(path, metadata.Medium); img != nil {
			return img
		}
	}
	if hasCover {
		u.p.RequestCover(path)
	}
	return nil
}

// drawImageCover paints img into r, scaled to cover it, clipped to a rounded rectangle.
func (u *UI) drawImageCover(ops *op.Ops, img *image.RGBA, r image.Rectangle, radius int, opacity float32) {
	if img == nil || r.Empty() {
		return
	}
	src := u.images.scaled(img, max(r.Dx(), r.Dy()))
	sb := src.Bounds()
	sw, sh := float32(sb.Dx()), float32(sb.Dy())
	scale := float32(math.Max(float64(float32(r.Dx())/sw), float64(float32(r.Dy())/sh)))
	off := f32.Pt(float32(r.Min.X)+(float32(r.Dx())-sw*scale)/2, float32(r.Min.Y)+(float32(r.Dy())-sh*scale)/2)
	defer clip.UniformRRect(r, radius).Push(ops).Pop()
	defer paint.PushOpacity(ops, opacity).Pop()
	defer op.Affine(f32.Affine2D{}.Scale(f32.Point{}, f32.Pt(scale, scale)).Offset(off)).Push(ops).Pop()
	iop := u.images.op(src)
	iop.Filter = paint.FilterLinear
	iop.Add(ops)
	paint.PaintOp{}.Add(ops)
}

// ---- now-playing equaliser ----

// eqBars draws the 12 dp three-bar equaliser: animated while playing, frozen at 3/8/5 dp when paused.
func eqBars(gtx layout.Context, origin image.Point, playing bool, c color.NRGBA) {
	bw, gap, full := dp(gtx, 2), dp(gtx, 2), float32(dp(gtx, 12))
	heights := [3]float32{3, 8, 5}
	if playing {
		t := float64(clk.now.UnixNano()) / 1e9
		for i := range heights {
			ph := (t - float64(i)*0.15) / 0.9 * 2 * math.Pi
			heights[i] = float32(3 + 9*(0.5+0.5*math.Sin(ph)))
		}
		clk.animating = true
	}
	for i, hh := range heights {
		h := int(hh / 12 * full)
		x := origin.X + i*(bw+gap)
		fillRRect(gtx.Ops, image.Rect(x, origin.Y+int(full)-h, x+bw, origin.Y+int(full)), bw/2, c)
	}
}
