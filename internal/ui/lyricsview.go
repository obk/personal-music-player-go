package ui

import (
	"fmt"
	"image"
	"math"
	"time"

	"gioui.org/font"
	"gioui.org/gesture"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/x/styledtext"

	"musicplayer/internal/lyrics"
)

// lyricsViewState is the karaoke-style synchronised lyrics view.
type lyricsViewState struct {
	lines      []lyrics.Line // the set currently shown (identity: a new track resets the view)
	emph       []fade        // per line: 1 while active (size, weight), animated
	dim        []fade        // per line opacity
	scrollY    float32       // offset of the viewport's centre from the first line's top
	animFrom   float32
	anim       tween
	lastActive int
	userAt     time.Time // last manual scroll; auto-follow resumes 3 s later
	userScroll bool
	scroll     gesture.Scroll
	click      gesture.Click
	hover      bool
	rects      []image.Rectangle // line rectangles in view coordinates, from the last layout
	minus      widget.Clickable
	value      widget.Clickable
	plus       widget.Clickable
	pill       fade
}

func (u *UI) layoutLyrics(gtx layout.Context, r image.Rectangle) {
	s := &u.lyr
	lines := u.p.Lyrics()
	if len(lines) == 0 {
		return
	}
	// New track -> new lyrics: start at the top, then glide to the active line.
	if len(s.lines) == 0 || &lines[0] != &s.lines[0] {
		s.lines = lines
		s.emph = make([]fade, len(lines))
		s.dim = make([]fade, len(lines))
		s.scrollY = 0
		s.lastActive = -2
		s.userScroll = false
	}
	active := u.p.ActiveLyric()
	word := u.p.ActiveWord()
	size := r.Size()
	w := size.X - gtx.Dp(8)

	defer u.push(gtx, r.Min)()
	defer clip.Rect(image.Rectangle{Max: size}).Push(gtx.Ops).Pop()

	// Measure every line (the view needs exact positions to centre one).
	spacing := gtx.Dp(14)
	calls := make([]op.CallOp, len(lines))
	heights := make([]int, len(lines))
	offs := make([]int, len(lines))
	y := 0
	lg := gtx
	lg.Constraints = layout.Constraints{Min: image.Pt(w, 0), Max: image.Pt(w, 1<<20)}
	for i, l := range lines {
		e := s.emph[i].to(boolf(i == active), normal)
		fs := float32(math.Round(float64(20+8*e)*2) / 2)
		calls[i], heights[i] = u.lyricLine(lg, l, i == active, word, fs)
		offs[i] = y
		y += heights[i] + spacing
	}
	total := y - spacing

	// Manual scrolling (wheel / touch) suspends following for a moment.
	if d := s.scroll.Update(gtx.Metric, gtx.Source, gtx.Now, gesture.Vertical,
		pointer.ScrollRange{}, pointer.ScrollRange{Min: -1 << 20, Max: 1 << 20}); d != 0 {
		s.scrollY = float32(math.Max(0, math.Min(float64(s.scrollY)+float64(d), float64(total))))
		s.userScroll, s.userAt = true, gtx.Now
		s.anim.running = false
	}
	if s.userScroll && gtx.Now.Sub(s.userAt) > 3*time.Second {
		s.userScroll = false
		s.lastActive = -2 // glide back to the active line
	}
	if s.userScroll {
		clk.animating = true // wake up to resume
	}

	// Follow: the active line's centre at the viewport's centre. Size changes of the active line move the
	// target while gliding, so the glide aims at the live target.
	if !s.userScroll && active >= 0 {
		target := float32(offs[active] + heights[active]/2)
		if active != s.lastActive {
			s.lastActive = active
			s.animFrom = s.scrollY
			s.anim.restart(450 * time.Millisecond)
		}
		if s.anim.running {
			p := easeInOutCubic(s.anim.progress())
			s.scrollY = s.animFrom + (target-s.animFrom)*p
		} else {
			s.scrollY = target
		}
	}

	// Click a line to jump to it.
	for {
		e, ok := s.click.Update(gtx.Source)
		if !ok {
			break
		}
		if e.Kind != gesture.KindClick {
			continue
		}
		pt := image.Pt(int(e.Position.X), int(e.Position.Y))
		for i, rr := range s.rects {
			if pt.In(rr) {
				u.p.SeekMs(max(0, lines[i].TimestampMs-int64(u.p.LyricsOffsetMs())))
				break
			}
		}
	}

	// Draw the visible lines; the top and bottom 14% fade out.
	s.rects = s.rects[:0]
	top := float32(size.Y)/2 - s.scrollY
	fadeZone := float32(size.Y) * 0.14
	for i := range lines {
		ly := int(top) + offs[i]
		rr := image.Rect(0, ly, w, ly+heights[i])
		s.rects = append(s.rects, rr)
		if rr.Max.Y < 0 || rr.Min.Y > size.Y {
			continue
		}
		distance := i - active
		if distance < 0 {
			distance = -distance
		}
		alpha := float32(1)
		if i != active {
			alpha = float32(math.Max(0.25, 0.6-float64(distance)*0.07))
		}
		alpha = s.dim[i].to(alpha, normal)
		c := float32(rr.Min.Y+rr.Max.Y) / 2
		edge := float32(1)
		if c < fadeZone {
			edge = c / fadeZone
		} else if c > float32(size.Y)-fadeZone {
			edge = (float32(size.Y) - c) / fadeZone
		}
		func() {
			defer paint.PushOpacity(gtx.Ops, alpha*clamp01(edge)).Pop()
			place(gtx.Ops, rr.Min, calls[i])
		}()
		pointerCursor(gtx, rr, pointer.CursorPointer)
	}

	// Input: clicks and scrolling over the whole view, hover for the offset pill.
	func() {
		defer clip.Rect(image.Rectangle{Max: size}).Push(gtx.Ops).Pop()
		s.click.Add(gtx.Ops)
		s.scroll.Add(gtx.Ops)
	}()
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &s.hover, Kinds: pointer.Enter | pointer.Leave})
		if !ok {
			break
		}
		if e, ok := ev.(pointer.Event); ok {
			s.hover = e.Kind == pointer.Enter
		}
	}
	u.offsetPill(gtx, size)
	func() {
		defer clip.Rect(image.Rectangle{Max: size}).Push(gtx.Ops).Pop()
		defer pointer.PassOp{}.Push(gtx.Ops).Pop()
		event.Op(gtx.Ops, &s.hover)
	}()
}

// lyricLine records one line. The active line of word-timed lyrics highlights the words sung so far.
func (u *UI) lyricLine(gtx layout.Context, l lyrics.Line, active bool, word int, size float32) (op.CallOp, int) {
	weight := font.Normal
	col := pal.text2
	if active {
		weight, col = font.Bold, pal.text1
	}
	c, d := record(gtx, func(gtx layout.Context) layout.Dimensions {
		if active && len(l.Words) > 0 {
			spans := make([]styledtext.SpanStyle, 0, len(l.Words))
			for i, wd := range l.Words {
				wc := withAlpha(pal.text1, 0.4)
				if i <= word {
					wc = pal.text1
				}
				spans = append(spans, styledtext.SpanStyle{Font: uiFont(weight), Size: unit.Sp(size), Color: wc, Content: wd.Text})
			}
			return styledtext.Text(u.shaper, spans...).Layout(gtx, nil)
		}
		txt := l.Text
		if txt == "" {
			txt = "♪"
		}
		return u.label(gtx, txt, textStyle{size: size, weight: weight, color: col, maxLines: -1})
	})
	return c, d.Size.Y
}

// offsetPill is the per-track timing correction (remembered per file), shown on hover or while an offset is set.
func (u *UI) offsetPill(gtx layout.Context, size image.Point) {
	s := &u.lyr
	offset := u.p.LyricsOffsetMs()
	if s.minus.Clicked(gtx) {
		u.p.SetLyricsOffsetMs(offset - 100)
	}
	if s.plus.Clicked(gtx) {
		u.p.SetLyricsOffsetMs(offset + 100)
	}
	if s.value.Clicked(gtx) {
		u.p.SetLyricsOffsetMs(0)
	}
	offset = u.p.LyricsOffsetMs()
	a := s.pill.to(boolf(s.hover || offset != 0), normal)
	if a <= 0.01 {
		return
	}
	valueText := "Lyrics offset"
	if offset != 0 {
		valueText = fmt.Sprintf("%+.1f s", float64(offset)/1000)
	}
	vc, vd := record(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min = image.Point{}
		return u.label(gtx, valueText, textStyle{size: fsColumn, weight: font.SemiBold, color: pal.text2})
	})
	bh := gtx.Dp(24)
	pad := gtx.Dp(3)
	sp := gtx.Dp(2)
	btnW := gtx.Dp(24)
	valueW := vd.Size.X + gtx.Dp(12)
	pw := pad*2 + btnW*2 + valueW + sp*2
	ph := gtx.Dp(30)
	origin := image.Pt(size.X-gtx.Dp(8)-pw, size.Y-gtx.Dp(8)-ph)
	defer u.push(gtx, origin)()
	defer paint.PushOpacity(gtx.Ops, a).Pop()
	fillRRect(gtx.Ops, image.Rect(0, 0, pw, ph), ph/2, pal.hover)

	button := func(x, width int, cl *widget.Clickable, content func(gtx layout.Context)) {
		u.at(gtx, image.Pt(x, (ph-bh)/2), image.Pt(width, bh), func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints = layout.Exact(image.Pt(width, bh))
			return cl.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				if cl.Hovered() {
					fillRRect(gtx.Ops, image.Rect(0, 0, width, bh), bh/2, pal.hover)
				}
				content(gtx)
				pointerCursor(gtx, image.Rect(0, 0, width, bh), pointer.CursorPointer)
				return layout.Dimensions{Size: image.Pt(width, bh)}
			})
		})
	}
	sign := func(sym string) func(gtx layout.Context) {
		return func(gtx layout.Context) {
			c, d := record(gtx, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min = image.Point{}
				return u.label(gtx, sym, textStyle{size: fsBody, weight: font.SemiBold, color: pal.text2})
			})
			place(gtx.Ops, image.Pt((btnW-d.Size.X)/2, (bh-d.Size.Y)/2), c)
		}
	}
	// − / + nudge by 100 ms (positive shows lyrics earlier); the value resets to 0.
	button(pad, btnW, &s.minus, sign("−"))
	button(pad+btnW+sp, valueW, &s.value, func(gtx layout.Context) {
		place(gtx.Ops, image.Pt((valueW-vd.Size.X)/2, (bh-vd.Size.Y)/2), vc)
	})
	button(pad+btnW+sp+valueW+sp, btnW, &s.plus, sign("+"))
}
