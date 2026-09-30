package ui

import (
	"fmt"
	"image"
	"strings"

	"gioui.org/font"
	"gioui.org/gesture"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"

	"musicplayer/internal/eq"
	"musicplayer/internal/player"
)

// ---- shared popup frame ----

type popupFrame struct {
	visible fade
}

// layoutPopups draws the open popup(s) above everything, with an outside-press layer that closes them.
func (u *UI) layoutPopups(gtx layout.Context, g geometry) {
	anyOpen := u.eqMenu.open || u.outMenu.open
	if anyOpen {
		// Outside press closes (the anchor buttons toggle by themselves). Events pass through, so the rest of
		// the window stays live.
		for {
			ev, ok := gtx.Event(pointer.Filter{Target: &u.eqMenu.dismiss, Kinds: pointer.Press})
			if !ok {
				break
			}
			e, ok := ev.(pointer.Event)
			if !ok {
				continue
			}
			pt := image.Pt(int(e.Position.X), int(e.Position.Y))
			if pt.In(u.eqMenu.rect) || pt.In(u.outMenu.rect) || pt.In(u.bar.eqAnchor) || pt.In(u.bar.outAnchor) {
				continue
			}
			u.eqMenu.open, u.outMenu.open = false, false
		}
		func() {
			defer clip.Rect(image.Rectangle{Max: g.ws}).Push(gtx.Ops).Pop()
			defer pointer.PassOp{}.Push(gtx.Ops).Pop()
			event.Op(gtx.Ops, &u.eqMenu.dismiss)
		}()
	}
	if !u.eqMenu.open {
		u.eqMenu.naming = false
	}
	u.layoutEqMenu(gtx, g)
	u.layoutOutputMenu(gtx, g)
}

// popup draws content in a surface card anchored above anchor, xShift from its right edge. It returns the rect.
func (u *UI) popup(gtx layout.Context, g geometry, f *popupFrame, open bool, width, padding int, anchor image.Rectangle,
	xShift int, content func(gtx layout.Context) layout.Dimensions) image.Rectangle {
	a := f.visible.to(boolf(open), fast)
	if a <= 0.01 {
		return image.Rectangle{}
	}
	cg := gtx
	cg.Constraints = layout.Constraints{Min: image.Pt(width-2*padding, 0), Max: image.Pt(width-2*padding, g.ws.Y)}
	if !open {
		cg = cg.Disabled()
	}
	c, d := record(cg, content)
	h := d.Size.Y + 2*padding
	x := anchor.Max.X - width + xShift
	y := anchor.Min.Y - h - gtx.Dp(16)
	x = min(max(x, gtx.Dp(8)), g.ws.X-width-gtx.Dp(8))
	y = max(y, gtx.Dp(8))
	r := image.Rect(x, y, x+width, y+h)
	defer paint.PushOpacity(gtx.Ops, a).Pop()
	shadow(gtx.Ops, r, dp(gtx, rPanel), dp(gtx, 24), dp(gtx, 8))
	fillRRect(gtx.Ops, r, dp(gtx, rPanel), pal.elevated)
	strokeRRect(gtx.Ops, r, dp(gtx, rPanel), 1, pal.divider)
	// Swallow presses on the card itself.
	func() {
		defer clip.Rect(r).Push(gtx.Ops).Pop()
		event.Op(gtx.Ops, f)
		for {
			if _, ok := gtx.Event(pointer.Filter{Target: f, Kinds: pointer.Press}); !ok {
				break
			}
		}
	}()
	place(gtx.Ops, r.Min.Add(image.Pt(padding, padding)), c)
	return r
}

func caption(u *UI, gtx layout.Context, s string) layout.Dimensions {
	return u.label(gtx, strings.ToUpper(s), textStyle{size: fsColumn, weight: font.SemiBold, color: pal.text3})
}

// vstack places widgets top to bottom with a gap and returns the total size.
func (u *UI) vstack(gtx layout.Context, gap int, ws ...layout.Widget) layout.Dimensions {
	y := 0
	for i, w := range ws {
		if i > 0 {
			y += gap
		}
		func() {
			defer u.push(gtx, image.Pt(0, y))()
			gg := gtx
			gg.Constraints.Min.Y = 0
			d := w(gg)
			y += d.Size.Y
		}()
	}
	return layout.Dimensions{Size: image.Pt(gtx.Constraints.Max.X, y)}
}

// ---- equalizer ----

// eqMenuState: on/off, presets, preamp + band sliders (+-12 dB). Changes apply live, ramped in the audio path.
type eqMenuState struct {
	open    bool
	frame   popupFrame
	rect    image.Rectangle
	dismiss int
	toggle  toggle
	chips   map[string]*chip
	save    chip
	reset   chip
	naming  bool
	namingN int
	editor  widget.Editor
	bands   [eq.Bands + 1]bandSlider
	dim     fade
}

func (u *UI) layoutEqMenu(gtx layout.Context, g geometry) {
	m := &u.eqMenu
	if m.chips == nil {
		m.chips = map[string]*chip{}
	}
	m.rect = u.popup(gtx, g, &m.frame, m.open, gtx.Dp(600), gtx.Dp(14), u.bar.eqAnchor, gtx.Dp(40), func(gtx layout.Context) layout.Dimensions {
		s := u.p.Equalizer()
		on := s.Enabled
		w := gtx.Constraints.Max.X
		return u.vstack(gtx, gtx.Dp(12),
			// Header.
			func(gtx layout.Context) layout.Dimensions {
				h := gtx.Dp(22)
				tc, td := record(gtx, func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min = image.Point{}
					return u.label(gtx, map[bool]string{true: "On", false: "Off"}[on], textStyle{size: fsCaption, color: pal.text2})
				})
				func() {
					defer u.push(gtx, image.Pt(w-gtx.Dp(38), 0))()
					if _, clicked := m.toggle.layout(gtx, on); clicked {
						u.p.SetEqEnabled(!on)
					}
				}()
				place(gtx.Ops, image.Pt(w-gtx.Dp(38)-gtx.Dp(8)-td.Size.X, (h-td.Size.Y)/2), tc)
				c, d := record(gtx, func(gtx layout.Context) layout.Dimensions { return caption(u, gtx, "Equalizer") })
				place(gtx.Ops, image.Pt(0, (h-d.Size.Y)/2), c)
				return layout.Dimensions{Size: image.Pt(w, h)}
			},
			// Presets; the one matching the current sliders is highlighted.
			func(gtx layout.Context) layout.Dimensions {
				current := u.p.EqPreset()
				var items []op.CallOp
				var widths []int
				for _, name := range u.p.EqPresets() {
					c := m.chips[name]
					if c == nil {
						c = &chip{}
						m.chips[name] = c
					}
					name := name
					call, d := record(gtx, func(gtx layout.Context) layout.Dimensions {
						d, clicked, removed := c.layout(u, gtx, name, on && current == name, !u.p.IsBuiltinEqPreset(name))
						if clicked {
							u.later(func() { u.p.ApplyEqPreset(name) })
						}
						if removed {
							u.later(func() { u.p.DeleteEqPreset(name) })
						}
						return d
					})
					items, widths = append(items, call), append(widths, d.Size.X)
				}
				call, d := record(gtx, func(gtx layout.Context) layout.Dimensions {
					if m.naming {
						return u.presetEditor(gtx)
					}
					d, clicked, _ := m.save.layout(u, gtx, "+ Save", false, false)
					if clicked {
						m.naming, m.namingN = true, 0
						m.editor.SetText("")
					}
					return d
				})
				items, widths = append(items, call), append(widths, d.Size.X)
				return flow(gtx, items, widths, gtx.Dp(28), gtx.Dp(6), false)
			},
			// Preamp | bands.
			func(gtx layout.Context) layout.Dimensions {
				a := m.dim.to(0.55+0.45*boolf(on), normal)
				defer paint.PushOpacity(gtx.Ops, a).Pop()
				labels := append([]string{"Pre"}, player.EqFrequencies()...)
				preW := gtx.Dp(40)
				divW := gtx.Dp(13)
				bandW := (w - preW - divW) / eq.Bands
				var h int
				for i := 0; i <= eq.Bands; i++ {
					x, bw := preW+divW+(i-1)*bandW, bandW
					value := s.PreampDb
					if i == 0 {
						x, bw = 0, preW
					} else {
						value = s.GainsDb[i-1]
					}
					i := i
					d := u.at(gtx, image.Pt(x, 0), image.Pt(bw, gtx.Dp(220)), func(gtx layout.Context) layout.Dimensions {
						d, v, moved := m.bands[i].layout(u, gtx, labels[i], value, on)
						if moved {
							if i == 0 {
								u.p.SetEqPreamp(v)
							} else {
								u.p.SetEqGain(i-1, v)
							}
						}
						return d
					})
					h = d.Size.Y
				}
				fillRect(gtx.Ops, image.Rect(preW+divW/2, gtx.Dp(18), preW+divW/2+1, h-gtx.Dp(18)), pal.divider)
				return layout.Dimensions{Size: image.Pt(w, h)}
			},
			// Hint and Reset.
			func(gtx layout.Context) layout.Dimensions {
				hint := "Double-click a slider to reset it."
				if u.p.ExclusiveMode() && on && u.p.EqPreset() != "Flat" {
					hint = "In exclusive mode the equalizer turns bit-perfect playback off (24-bit dithered output)."
				}
				rc, rd := record(gtx, func(gtx layout.Context) layout.Dimensions {
					d, clicked, _ := m.reset.layout(u, gtx, "Reset", false, false)
					if clicked {
						u.later(u.p.ResetEq)
					}
					return d
				})
				gg := gtx
				gg.Constraints.Max.X = w - rd.Size.X - gtx.Dp(8)
				gg.Constraints.Min = image.Point{}
				hc, hd := record(gg, func(gtx layout.Context) layout.Dimensions {
					return u.label(gtx, hint, textStyle{size: fsColumn, color: pal.text3, maxLines: -1})
				})
				h := max(hd.Size.Y, rd.Size.Y)
				place(gtx.Ops, image.Pt(0, (h-hd.Size.Y)/2), hc)
				place(gtx.Ops, image.Pt(w-rd.Size.X, (h-rd.Size.Y)/2), rc)
				return layout.Dimensions{Size: image.Pt(w, h)}
			},
		)
	})
}

// presetEditor is the inline name field: Enter saves, Escape or clicking elsewhere cancels.
func (u *UI) presetEditor(gtx layout.Context) layout.Dimensions {
	m := &u.eqMenu
	m.editor.SingleLine, m.editor.Submit, m.editor.MaxLen = true, true, 32
	w, h := gtx.Dp(180), gtx.Dp(28)
	if m.namingN == 0 {
		gtx.Execute(key.FocusCmd{Tag: &m.editor})
	} else if m.namingN > 2 && !gtx.Focused(&m.editor) {
		u.later(func() { m.naming = false })
	}
	m.namingN++
	for {
		ev, ok := m.editor.Update(gtx)
		if !ok {
			break
		}
		if _, ok := ev.(widget.SubmitEvent); ok {
			name := m.editor.Text()
			u.later(func() {
				u.p.SaveEqPreset(name)
				m.naming = false
			})
		}
	}
	fillRRect(gtx.Ops, image.Rect(0, 0, w, h), h/2, pal.hover)
	strokeRRect(gtx.Ops, image.Rect(0, 0, w, h), h/2, float32(gtx.Dp(1)), pal.accent)
	u.at(gtx, image.Pt(gtx.Dp(10), 0), image.Pt(w-gtx.Dp(20), h), func(gtx layout.Context) layout.Dimensions {
		if m.editor.Len() == 0 {
			c, d := record(gtx, func(gtx layout.Context) layout.Dimensions {
				return u.label(gtx, "Preset name, Enter to save", textStyle{size: fsCaption, color: pal.text3})
			})
			place(gtx.Ops, image.Pt(0, (h-d.Size.Y)/2), c)
		}
		gtx.Constraints.Min.Y = 0
		c, d := record(gtx, func(gtx layout.Context) layout.Dimensions {
			return m.editor.Layout(gtx, u.shaper, uiFont(font.Normal), unit.Sp(fsCaption),
				colorMaterial(gtx.Ops, pal.text1), colorMaterial(gtx.Ops, withAlpha(pal.accent, 0.4)))
		})
		place(gtx.Ops, image.Pt(0, (h-d.Size.Y)/2), c)
		return layout.Dimensions{Size: image.Pt(w, h)}
	})
	return layout.Dimensions{Size: image.Pt(w, h)}
}

// bandSlider is a vertical dB slider: value on top, a rail with a centre (0 dB) mark and a fill from 0 dB to the
// value, the label below. Double-click resets to 0 dB.
type bandSlider struct {
	drag    gesture.Drag
	click   gesture.Click
	hover   gesture.Hover
	pressed bool
	scale   spring
}

func (b *bandSlider) layout(u *UI, gtx layout.Context, label string, value float64, on bool) (layout.Dimensions, float64, bool) {
	w := gtx.Constraints.Max.X
	railH := gtx.Dp(150)
	knob := gtx.Dp(16)
	vc, vd := record(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min = image.Point{}
		col := pal.text2
		if value == 0 {
			col = pal.text3
		}
		return u.label(gtx, fmt.Sprintf("%+.1f", value), textStyle{size: fsColumn, color: col})
	})
	lc, ld := record(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Min = image.Point{}
		return u.label(gtx, label, textStyle{size: fsColumn, weight: font.Medium, color: pal.text3, align: text.Middle})
	})
	top := vd.Size.Y + gtx.Dp(4)
	sliderTop := top
	moved := false
	toValue := func(y float32) float64 {
		p := float64((y - float32(knob)/2) / float32(railH-knob))
		v := 12 - 24*max(0, min(1, p))
		return float64(int(v*2+sign(v)*0.5)) / 2 // 0.5 dB steps
	}
	for {
		e, ok := b.drag.Update(gtx.Metric, gtx.Source, gesture.Vertical)
		if !ok {
			break
		}
		switch e.Kind {
		case pointer.Press:
			b.pressed = true
			value, moved = toValue(e.Position.Y), true
		case pointer.Drag:
			value, moved = toValue(e.Position.Y), true
		case pointer.Release, pointer.Cancel:
			b.pressed = false
		}
	}
	for {
		e, ok := b.click.Update(gtx.Source)
		if !ok {
			break
		}
		if e.Kind == gesture.KindClick && e.NumClicks == 2 {
			value, moved = 0, true
		}
	}
	hovered := b.hover.Update(gtx.Source)

	cx := w / 2
	railW := gtx.Dp(4)
	fillRRect(gtx.Ops, image.Rect(cx-railW/2, sliderTop, cx+railW/2, sliderTop+railH), railW/2, pal.hover)
	zeroY := sliderTop + railH/2
	fillRect(gtx.Ops, image.Rect(cx-gtx.Dp(6), zeroY, cx+gtx.Dp(6), zeroY+1), pal.text3)
	pos := float32((12 - value) / 24)
	valueY := sliderTop + knob/2 + int(pos*float32(railH-knob))
	fillCol := pal.text3
	if on {
		fillCol = pal.accent
	}
	fillRRect(gtx.Ops, image.Rect(cx-railW/2, min(zeroY, valueY), cx+railW/2, max(zeroY, valueY)), railW/2, fillCol)
	target := float32(1)
	if b.pressed {
		target = 1.15
	} else if hovered {
		target = 1.05
	}
	s := b.scale.to(target)
	fillCircle(gtx.Ops, image.Pt(cx, valueY), int(float32(knob/2)*s), pal.text1)

	area := clip.Rect(image.Rect(0, sliderTop, w, sliderTop+railH)).Push(gtx.Ops)
	b.drag.Add(gtx.Ops)
	b.click.Add(gtx.Ops)
	b.hover.Add(gtx.Ops)
	pointer.CursorPointer.Add(gtx.Ops)
	area.Pop()

	place(gtx.Ops, image.Pt((w-vd.Size.X)/2, 0), vc)
	place(gtx.Ops, image.Pt((w-ld.Size.X)/2, sliderTop+railH+gtx.Dp(4)), lc)
	return layout.Dimensions{Size: image.Pt(w, sliderTop+railH+gtx.Dp(4)+ld.Size.Y)}, value, moved
}

func sign(v float64) float64 {
	if v < 0 {
		return -1
	}
	return 1
}

// ---- output ----

// outMenuState: device list, exclusive (bit-perfect) mode, and the format the device is actually running at.
type outMenuState struct {
	open   bool
	frame  popupFrame
	rect   image.Rectangle
	rows   map[string]*deviceRow
	toggle toggle
}

type deviceRow struct {
	click widget.Clickable
	tint  fade
}

func (u *UI) layoutOutputMenu(gtx layout.Context, g geometry) {
	m := &u.outMenu
	if m.rows == nil {
		m.rows = map[string]*deviceRow{}
	}
	m.rect = u.popup(gtx, g, &m.frame, m.open, gtx.Dp(320), gtx.Dp(8), u.bar.outAnchor, 0, func(gtx layout.Context) layout.Dimensions {
		w := gtx.Constraints.Max.X
		pad := gtx.Dp(10)
		ws := []layout.Widget{
			func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Max.X = w - 2*gtx.Dp(8)
				c, d := record(gtx, func(gtx layout.Context) layout.Dimensions { return caption(u, gtx, "Output") })
				place(gtx.Ops, image.Pt(gtx.Dp(8), gtx.Dp(8)), c)
				return layout.Dimensions{Size: image.Pt(w, d.Size.Y+gtx.Dp(12))}
			},
		}
		selected := u.p.OutputDeviceID()
		for _, dev := range u.p.OutputDevices() {
			dev := dev
			row := m.rows[dev.ID]
			if row == nil {
				row = &deviceRow{}
				m.rows[dev.ID] = row
			}
			ws = append(ws, func(gtx layout.Context) layout.Dimensions {
				if row.click.Clicked(gtx) {
					id := dev.ID
					u.later(func() { u.p.SetOutputDevice(id) })
				}
				h := gtx.Dp(36)
				if dev.Detail != "" {
					h = gtx.Dp(48)
				}
				isSel := dev.ID == selected
				gtx.Constraints = layout.Exact(image.Pt(w, h))
				return row.click.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					bg := withAlpha(pal.hover, row.tint.to(boolf(row.click.Hovered()), fast))
					if isSel {
						bg = pal.selected
					}
					fillRRect(gtx.Ops, image.Rect(0, 0, w, h), gtx.Dp(10), bg)
					checkW := 0
					if isSel {
						cc, cd := record(gtx, func(gtx layout.Context) layout.Dimensions {
							gtx.Constraints.Min = image.Point{}
							return u.label(gtx, icCheck, textStyle{size: 18, color: pal.accent, icon: true})
						})
						checkW = cd.Size.X + gtx.Dp(8)
						place(gtx.Ops, image.Pt(w-pad-cd.Size.X, (h-cd.Size.Y)/2), cc)
					}
					gg := gtx
					gg.Constraints = layout.Constraints{Max: image.Pt(max(0, w-2*pad-checkW), h)}
					weight := font.Normal
					if isSel {
						weight = font.SemiBold
					}
					nc, nd := record(gg, func(gtx layout.Context) layout.Dimensions {
						return u.label(gtx, dev.Name, textStyle{size: fsCell, weight: weight, color: pal.text1})
					})
					var dc op.CallOp
					var dd layout.Dimensions
					if dev.Detail != "" {
						dc, dd = record(gg, func(gtx layout.Context) layout.Dimensions {
							return u.label(gtx, dev.Detail, textStyle{size: fsColumn, color: pal.text3})
						})
					}
					total := nd.Size.Y + dd.Size.Y
					if dd.Size.Y > 0 {
						total += gtx.Dp(1)
					}
					y := (h - total) / 2
					place(gtx.Ops, image.Pt(pad, y), nc)
					if dd.Size.Y > 0 {
						place(gtx.Ops, image.Pt(pad, y+nd.Size.Y+gtx.Dp(1)), dc)
					}
					pointerCursor(gtx, image.Rect(0, 0, w, h), pointer.CursorPointer)
					return layout.Dimensions{Size: image.Pt(w, h)}
				})
			})
		}
		divider := func(gtx layout.Context) layout.Dimensions {
			fillRect(gtx.Ops, image.Rect(0, gtx.Dp(6), w, gtx.Dp(6)+1), pal.divider)
			return layout.Dimensions{Size: image.Pt(w, gtx.Dp(13))}
		}
		ws = append(ws, divider,
			// Exclusive mode.
			func(gtx layout.Context) layout.Dimensions {
				iw := w - 2*pad
				tc, td := record(gtx, func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min = image.Point{}
					return u.label(gtx, "Exclusive mode", textStyle{size: fsCell, weight: font.SemiBold, color: pal.text1})
				})
				rowH := max(td.Size.Y, gtx.Dp(22))
				gg := gtx
				gg.Constraints = layout.Constraints{Max: image.Pt(iw, 1000)}
				dc, dd := record(gg, func(gtx layout.Context) layout.Dimensions {
					return u.label(gtx, "Bypasses the Windows mixer and plays each file at its own sample rate and bit depth. "+
						"Other apps can't play sound meanwhile, and volume is fixed at 100%.",
						textStyle{size: fsColumn, color: pal.text3, maxLines: -1})
				})
				y := gtx.Dp(6)
				place(gtx.Ops, image.Pt(pad, y+(rowH-td.Size.Y)/2), tc)
				func() {
					defer u.push(gtx, image.Pt(w-pad-gtx.Dp(38), y+(rowH-gtx.Dp(22))/2))()
					if _, clicked := m.toggle.layout(gtx, u.p.ExclusiveMode()); clicked {
						on := !u.p.ExclusiveMode()
						u.later(func() { u.p.SetExclusiveMode(on) })
					}
				}()
				place(gtx.Ops, image.Pt(pad, y+rowH+gtx.Dp(4)), dc)
				return layout.Dimensions{Size: image.Pt(w, y+rowH+gtx.Dp(4)+dd.Size.Y+gtx.Dp(6))}
			},
			divider,
			// The actual output format (driver-reported).
			func(gtx layout.Context) layout.Dimensions {
				f := u.p.OutputFormat()
				summary := u.p.OutputSummary()
				if summary == "" {
					summary = "Not playing"
				}
				badgeW := 0
				var bc op.CallOp
				var bdim layout.Dimensions
				if f.BitPerfect {
					bc, bdim = record(gtx, func(gtx layout.Context) layout.Dimensions {
						lc, ld := record(gtx, func(gtx layout.Context) layout.Dimensions {
							gtx.Constraints.Min = image.Point{}
							return u.label(gtx, "Bit-perfect", textStyle{size: fsColumn, weight: font.SemiBold, color: pal.accentHover})
						})
						bw, bh := ld.Size.X+gtx.Dp(16), gtx.Dp(22)
						fillRRect(gtx.Ops, image.Rect(0, 0, bw, bh), bh/2, withAlpha(pal.accent, 0.2))
						place(gtx.Ops, image.Pt(gtx.Dp(8), (bh-ld.Size.Y)/2), lc)
						return layout.Dimensions{Size: image.Pt(bw, bh)}
					})
					badgeW = bdim.Size.X + gtx.Dp(8)
				}
				gg := gtx
				gg.Constraints = layout.Constraints{Max: image.Pt(max(0, w-2*pad-badgeW), 100)}
				sc, sd := record(gg, func(gtx layout.Context) layout.Dimensions {
					return u.label(gtx, summary, textStyle{size: fsCaption, weight: font.Medium, color: pal.text2})
				})
				var nc op.CallOp
				var nd layout.Dimensions
				if f.DeviceName != "" {
					nc, nd = record(gg, func(gtx layout.Context) layout.Dimensions {
						return u.label(gtx, f.DeviceName, textStyle{size: fsColumn, color: pal.text3})
					})
				}
				h := sd.Size.Y + nd.Size.Y + gtx.Dp(1)
				place(gtx.Ops, image.Pt(pad, 0), sc)
				if nd.Size.Y > 0 {
					place(gtx.Ops, image.Pt(pad, sd.Size.Y+gtx.Dp(1)), nc)
				}
				if f.BitPerfect {
					place(gtx.Ops, image.Pt(w-pad-bdim.Size.X, (h-bdim.Size.Y)/2), bc)
				}
				return layout.Dimensions{Size: image.Pt(w, h+gtx.Dp(6))}
			},
		)
		return u.vstack(gtx, gtx.Dp(2), ws...)
	})
}
