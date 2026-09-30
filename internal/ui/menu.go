package ui

import (
	"image"

	"gioui.org/font"
	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"gioui.org/widget"
)

// menuItem is one entry of a context menu. A separator or header item draws only a line / a caption.
type menuItem struct {
	label, glyph, shortcut string
	danger, disabled       bool
	separator              bool
	sub                    func() []menuItem // a submenu, built when it opens
	do                     func()
}

type menuState struct {
	open    bool
	pos     image.Point
	header  string
	items   []menuItem
	clicks  []widget.Clickable
	rect    image.Rectangle
	sub     int // index of the item whose submenu is open, or -1
	subList []menuItem
	subPos  image.Point
	subRect image.Rectangle
	subClk  []widget.Clickable
	dismiss int
}

// openMenu shows a context menu at a window position; header (if any) is a caption line such as "3 tracks".
func (u *UI) openMenu(at image.Point, header string, items []menuItem) {
	m := &u.menu
	*m = menuState{open: true, pos: at, header: header, items: items, sub: -1,
		clicks: make([]widget.Clickable, len(items))}
	u.tooltip.key = nil
}

func (u *UI) layoutMenu(gtx layout.Context) {
	m := &u.menu
	if !m.open {
		return
	}
	// Outside press closes (and is swallowed, as with native menus).
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &m.dismiss, Kinds: pointer.Press})
		if !ok {
			break
		}
		if e, ok := ev.(pointer.Event); ok {
			p := e.Position.Round()
			if !p.In(m.rect) && !p.In(m.subRect) {
				m.open = false
				return
			}
		}
	}
	func() {
		defer clip.Rect(image.Rectangle{Max: u.size}).Push(gtx.Ops).Pop()
		event.Op(gtx.Ops, &m.dismiss)
	}()

	m.rect = u.drawMenu(gtx, m.pos, m.header, m.items, m.clicks, true, image.Rectangle{})
	if m.sub >= 0 && m.sub < len(m.items) {
		if len(m.subClk) != len(m.subList) {
			m.subClk = make([]widget.Clickable, len(m.subList))
		}
		m.subRect = u.drawMenu(gtx, m.subPos, "", m.subList, m.subClk, false, m.rect)
	} else {
		m.subRect = image.Rectangle{}
	}
}

// drawMenu lays out one menu panel with its top-left at pos (moved to stay on screen) and returns its rectangle.
// parent is the rectangle of the menu a submenu belongs to.
func (u *UI) drawMenu(gtx layout.Context, pos image.Point, header string, items []menuItem, clicks []widget.Clickable,
	top bool, parent image.Rectangle) image.Rectangle {
	m := &u.menu
	w := dp(gtx, 260)
	pad := dp(gtx, 4)
	itemH, sepH, headH := dp(gtx, 32), dp(gtx, 9), dp(gtx, 28)
	h := 2 * pad
	if header != "" {
		h += headH
	}
	for _, it := range items {
		if it.separator {
			h += sepH
		} else {
			h += itemH
		}
	}
	ws := u.size
	if !parent.Empty() && pos.X+w > ws.X {
		pos.X = parent.Min.X - w + pad // no room on the right: open to the left
	}
	pos.X = max(dp(gtx, 4), min(pos.X, ws.X-w-dp(gtx, 4)))
	pos.Y = max(dp(gtx, 4), min(pos.Y, ws.Y-h-dp(gtx, 4)))
	r := image.Rect(pos.X, pos.Y, pos.X+w, pos.Y+h)
	shadow(gtx.Ops, r, dp(gtx, rPanel), dp(gtx, 24), dp(gtx, 8))
	fillRRect(gtx.Ops, r, dp(gtx, rPanel), pal.elevated)
	if pal.light {
		strokeRRect(gtx.Ops, r, dp(gtx, rPanel), 1, pal.divider)
	}
	// Swallow presses on the panel itself.
	func() {
		defer clip.Rect(r).Push(gtx.Ops).Pop()
		event.Op(gtx.Ops, &clicks)
	}()

	y := r.Min.Y + pad
	if header != "" {
		c, d := u.text(gtx, header, textStyle{size: fsColumn, weight: font.SemiBold, color: pal.text3})
		place(gtx.Ops, image.Pt(r.Min.X+dp(gtx, 12), y+(headH-d.Size.Y)/2), c)
		y += headH
	}
	for i, it := range items {
		if it.separator {
			fillRect(gtx.Ops, image.Rect(r.Min.X+pad, y+sepH/2, r.Max.X-pad, y+sepH/2+1), pal.divider)
			y += sepH
			continue
		}
		row := image.Rect(r.Min.X+pad, y, r.Max.X-pad, y+itemH)
		cl := &clicks[i]
		gg := gtx
		if it.disabled {
			gg = gtx.Disabled()
		}
		if cl.Clicked(gg) && !it.disabled {
			if it.sub != nil {
				m.sub, m.subList, m.subPos = i, it.sub(), image.Pt(row.Max.X-pad, row.Min.Y-pad)
				m.subClk = nil
			} else {
				m.open = false
				if it.do != nil {
					u.later(it.do)
				}
			}
		}
		hovered := cl.Hovered() && !it.disabled
		if top && hovered {
			// Hovering an item opens its submenu, or closes another one.
			if it.sub != nil && m.sub != i {
				m.sub, m.subList, m.subPos, m.subClk = i, it.sub(), image.Pt(row.Max.X-pad, row.Min.Y-pad), nil
			} else if it.sub == nil {
				m.sub = -1
			}
		}
		u.at(gg, row.Min, row.Size(), func(gtx layout.Context) layout.Dimensions {
			sz := row.Size()
			gtx.Constraints = layout.Exact(sz)
			return cl.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				open := top && m.sub == i
				if hovered || open {
					fillRRect(gtx.Ops, image.Rectangle{Max: sz}, dp(gtx, rSmall), pal.hover)
				}
				col, icol := pal.text1, pal.text2
				if it.danger {
					col, icol = pal.danger, pal.danger
				}
				opacity := float32(1)
				if it.disabled {
					opacity = 0.4
				}
				defer paintOpacity(gtx, opacity).Pop()
				if it.glyph != "" {
					c, d := u.icon(gtx, it.glyph, 16, icol)
					place(gtx.Ops, image.Pt(dp(gtx, 8), (sz.Y-d.Size.Y)/2), c)
				}
				right := sz.X - dp(gtx, 8)
				if it.sub != nil {
					c, d := u.icon(gtx, icChevron, 16, pal.text2)
					right -= d.Size.X
					place(gtx.Ops, image.Pt(right, (sz.Y-d.Size.Y)/2), c)
				} else if it.shortcut != "" {
					c, d := u.text(gtx, it.shortcut, textStyle{size: fsCaption, color: pal.text3})
					right -= d.Size.X
					place(gtx.Ops, image.Pt(right, (sz.Y-d.Size.Y)/2), c)
				}
				lx := dp(gtx, 36)
				u.cell(gtx, it.label, textStyle{size: fsCell, color: col}, image.Pt(lx, 0), right-lx-dp(gtx, 8), sz.Y, pal.hover)
				if !it.disabled {
					pointerCursor(gtx, image.Rectangle{Max: sz}, pointer.CursorPointer)
				}
				return layout.Dimensions{Size: sz}
			})
		})
		y += itemH
	}
	return r
}
