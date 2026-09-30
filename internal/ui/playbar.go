package ui

import (
	"image"
	"math"
	"time"

	"gioui.org/font"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/text"

	"musicplayer/internal/library"
	"musicplayer/internal/queue"
)

// barState is the full-width playback bar: track (with like) | transport + progress | queue, EQ, output, volume.
type barState struct {
	like, shuffle, prev, play, next, repeat iconButton
	queue, eq, output, mute                 iconButton
	seek, volume                            slider
	settleUntil                             time.Time // after a scrub, follow playback again once this passes
	seekShown                               float64   // ms shown on the progress bar
	lastVolume                              float64
	eqAnchor, outAnchor                     image.Rectangle // window coordinates of the popup buttons
	info                                    trackInfoArea
}

type trackInfoArea struct {
	hovered bool
}

func (u *UI) layoutBar(gtx layout.Context, g geometry) {
	b := &u.bar
	r := g.bar
	fillRect(gtx.Ops, r, pal.surface)
	fillRect(gtx.Ops, image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Min.Y+1), pal.divider)
	defer u.push(gtx, r.Min)()
	w, h := r.Dx(), r.Dy()
	pad := dp(gtx, 16)

	leftW := max(dp(gtx, 240), int(float32(w)*0.3))
	leftW = min(leftW, int(float32(w)*0.3))
	leftW = max(leftW, dp(gtx, 240))
	centerW := min(dp(gtx, 640), w-2*leftW)
	centerX := (w - centerW) / 2

	// ── Left: the track.
	if t := u.p.Current(); t != nil {
		a := dp(gtx, 56)
		ar := image.Rect(pad, (h-a)/2, pad+a, (h+a)/2)
		cover := u.p.Cover("medium")
		u.artwork(gtx, cover, albumKeyOf(t), ar, dp(gtx, rSmall))
		tx := ar.Max.X + dp(gtx, 12)
		likeW := dp(gtx, 32)
		tw := centerX - dp(gtx, 16) - tx - likeW - dp(gtx, 8)
		lh := dp(gtx, 20)
		top := (h - lh - dp(gtx, 16)) / 2
		if u.cell(gtx, t.Info.Title, textStyle{size: fsBody, weight: font.SemiBold, color: pal.text1}, image.Pt(tx, top), tw, lh, pal.surface) &&
			u.mouse.pos.In(u.wr(image.Rect(tx, top, tx+tw, top+lh))) {
			u.tipAtMouse(&b.info, t.Info.Title)
		}
		artist := orUnknown(t.Info.Artist, "Unknown Artist")
		if u.cell(gtx, artist, textStyle{size: fsCaption, color: pal.text2}, image.Pt(tx, top+lh), tw, dp(gtx, 16), pal.surface) &&
			u.mouse.pos.In(u.wr(image.Rect(tx, top+lh, tx+tw, top+lh+dp(gtx, 16)))) {
			u.tipAtMouse(&b.info.hovered, artist)
		}
		liked := u.lib.IsLiked(t.ID)
		func() {
			defer u.push(gtx, image.Pt(tx+tw+dp(gtx, 8), (h-likeW)/2))()
			glyph := icHeartLine
			if liked {
				glyph = icHeart
			}
			st := iconBtn(glyph, 20)
			st.size = 32
			st.active = liked
			if _, clicked := b.like.layout(u, gtx, st); clicked {
				id := t.ID
				u.later(func() { u.p.ToggleLike(id) })
			}
			tipText := "Add to Liked Songs"
			if liked {
				tipText = "Remove from Liked Songs"
			}
			u.tipButton(gtx, &b.like, tipText, image.Rect(0, 0, likeW, likeW))
		}()
	}

	// ── Centre: transport and progress.
	func() {
		defer u.push(gtx, image.Pt(centerX, 0))()
		type ctl struct {
			btn *iconButton
			st  iconStyle
			tip string
			do  func()
		}
		shuffle := iconBtn(icShuffle, 20)
		shuffle.active = u.p.Shuffle()
		prev := iconBtn(icPrev, 24)
		prev.color = pal.text1
		next := iconBtn(icNext, 24)
		next.color = pal.text1
		repeat := iconBtn(icRepeat, 20)
		repeatTip := "Repeat: off"
		switch u.p.RepeatMode() {
		case queue.RepeatAll:
			repeat.active, repeatTip = true, "Repeat: all"
		case queue.RepeatOne:
			repeat.active, repeat.glyph, repeatTip = true, icRepeatOne, "Repeat: one"
		}
		playGlyph := icPlay
		if u.p.Playing() {
			playGlyph = icPause
		}
		ctls := []ctl{
			{&b.shuffle, shuffle, "Shuffle", u.p.ToggleShuffle},
			{&b.prev, prev, "Previous (Ctrl+←)", u.p.Previous},
			{&b.play, iconStyle{glyph: playGlyph, iconSize: 26, size: 40, filled: true}, "Play / Pause (Space)", u.p.TogglePlayPause},
			{&b.next, next, "Next (Ctrl+→)", u.p.Next},
			{&b.repeat, repeat, repeatTip, u.p.CycleRepeat},
		}
		gap := dp(gtx, 16)
		total := -gap
		for _, c := range ctls {
			total += dp(gtx, sizeOf(c.st)) + gap
		}
		rowH := dp(gtx, 40)
		top := dp(gtx, 10)
		x := (centerW - total) / 2
		for _, c := range ctls {
			s := dp(gtx, sizeOf(c.st))
			func() {
				defer u.push(gtx, image.Pt(x, top+(rowH-s)/2))()
				if _, clicked := c.btn.layout(u, gtx, c.st); clicked {
					u.later(c.do)
				}
				u.tipButton(gtx, c.btn, c.tip, image.Rect(0, 0, s, s))
			}()
			x += s + gap
		}

		// Progress: follows playback, but not while the user scrubs (or just after, until the engine reports the new
		// position).
		duration := u.p.Duration()
		y := top + rowH + dp(gtx, 8) - dp(gtx, 6)
		labelW := dp(gtx, 48)
		if !b.seek.pressed && gtx.Now.After(b.settleUntil) {
			b.seekShown = u.positionMs
		}
		frac := float32(0)
		if duration > 0 {
			frac = float32(b.seekShown / float64(duration))
		}
		sx := labelW + dp(gtx, 8)
		sw := centerW - 2*sx
		var moved bool
		var nf float32
		u.at(gtx, image.Pt(sx, y), image.Pt(sw, dp(gtx, 24)), func(gtx layout.Context) layout.Dimensions {
			d, v, m := b.seek.layout(gtx, frac, duration > 0)
			nf, moved = v, m
			if b.seek.hovered && duration > 0 {
				// The time under the pointer, above it.
				at := float64(b.seek.hoverX) / float64(max(1, sw)) * float64(duration)
				p := u.wr(image.Rect(int(b.seek.hoverX), -dp(gtx, 30), int(b.seek.hoverX)+1, -dp(gtx, 30)))
				u.tip(&b.seek, library.FormatDuration(int64(math.Max(0, at))), p)
				u.tooltip.since = u.tooltip.since.Add(-time.Second) // no delay for the scrub time
			}
			return d
		})
		if moved {
			ms := math.Round(float64(nf) * float64(duration))
			b.seekShown = ms
			u.p.SeekMs(int64(ms))
		}
		if b.seek.pressed {
			b.settleUntil = gtx.Now.Add(250 * time.Millisecond)
		}
		tl := textStyle{size: fsTime, weight: font.Medium, color: pal.text2}
		lc, ld := u.text(gtx, library.FormatDuration(int64(b.seekShown)), tl)
		place(gtx.Ops, image.Pt(labelW-ld.Size.X, y+(dp(gtx, 24)-ld.Size.Y)/2), lc)
		st := tl
		st.align = text.Start
		rc, rd := u.text(gtx, library.FormatDuration(duration), st)
		place(gtx.Ops, image.Pt(centerW-labelW, y+(dp(gtx, 24)-rd.Size.Y)/2), rc)
	}()

	// ── Right: queue, equalizer, output, volume.
	func() {
		volW := dp(gtx, 100)
		btn := dp(gtx, 32)
		gap := dp(gtx, 4)
		x := w - pad - volW - gap - btn - 3*(btn+gap)
		y := (h - btn) / 2
		items := []struct {
			btn    *iconButton
			glyph  string
			active bool
			tip    string
			do     func()
			anchor *image.Rectangle
		}{
			{&b.queue, icQueue, u.queueOpen, "Queue", func() { u.setQueueOpen(!u.queueOpen) }, nil},
			{&b.eq, icTune, u.p.Equalizer().Enabled || u.eqMenu.open, "Equalizer", func() {
				u.eqMenu.open, u.outMenu.open = !u.eqMenu.open, false
			}, &b.eqAnchor},
			{&b.output, icSpeaker, u.p.ExclusiveMode() || u.outMenu.open, "Output device", func() {
				u.outMenu.open, u.eqMenu.open = !u.outMenu.open, false
			}, &b.outAnchor},
		}
		for _, it := range items {
			func() {
				defer u.push(gtx, image.Pt(x, y))()
				st := iconBtn(it.glyph, 20)
				st.size = 32
				st.active = it.active
				if _, clicked := it.btn.layout(u, gtx, st); clicked {
					u.later(it.do)
				}
				u.tipButton(gtx, it.btn, it.tip, image.Rect(0, 0, btn, btn))
				if it.anchor != nil {
					*it.anchor = u.wr(image.Rect(0, 0, btn, btn))
				}
			}()
			x += btn + gap
		}
		// Volume (fixed at 100% in exclusive mode: samples go out untouched).
		locked := u.p.VolumeLocked()
		vol := u.p.Volume()
		glyph := icVolumeUp
		if vol == 0 {
			glyph = icVolumeOff
		} else if vol < 0.5 {
			glyph = icVolumeDown
		}
		func() {
			defer u.push(gtx, image.Pt(x, y))()
			st := iconBtn(glyph, 20)
			st.size = 32
			st.disabled = locked
			if _, clicked := b.mute.layout(u, gtx, st); clicked {
				if vol > 0 {
					b.lastVolume = vol
					u.p.SetVolume(0)
				} else {
					u.p.SetVolume(map[bool]float64{true: 0.8, false: b.lastVolume}[b.lastVolume == 0])
				}
			}
			if locked {
				u.tipButton(gtx, &b.mute, "Volume is fixed at 100% in exclusive mode", image.Rect(0, 0, btn, btn))
			}
		}()
		x += btn + gap
		shown := float32(vol)
		if locked {
			shown = 1
		}
		u.at(gtx, image.Pt(x, (h-dp(gtx, 24))/2), image.Pt(volW, dp(gtx, 24)), func(gtx layout.Context) layout.Dimensions {
			d, v, m := b.volume.layout(gtx, shown, !locked)
			if m {
				u.p.SetVolume(float64(v))
			}
			return d
		})
	}()
	pointerCursor(gtx, image.Rectangle{}, pointer.CursorDefault)
}

func sizeOf(st iconStyle) float32 {
	if st.size != 0 {
		return st.size
	}
	return st.iconSize + 12
}
