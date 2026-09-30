package ui

import (
	"image"

	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
)

// Gio does not expose the current transform, but tooltips, menus and drop targets need window coordinates. The
// UI therefore pushes offsets through push, which keeps u.org (the current origin in window coordinates) in step.

// push offsets the drawing origin by pt; call the returned function to undo it.
func (u *UI) push(gtx layout.Context, pt image.Point) func() {
	st := op.Offset(pt).Push(gtx.Ops)
	u.org = u.org.Add(pt)
	return func() {
		st.Pop()
		u.org = u.org.Sub(pt)
	}
}

// at runs w with its origin at pt and the given maximum size.
func (u *UI) at(gtx layout.Context, pt, size image.Point, w layout.Widget) layout.Dimensions {
	defer u.push(gtx, pt)()
	gtx.Constraints = layout.Constraints{Max: size}
	return w(gtx)
}

// win converts a rectangle in the current coordinates to window coordinates.
func (u *UI) wr(r image.Rectangle) image.Rectangle { return r.Add(u.org) }

// mouseTracker follows the pointer in window coordinates through a full-window pass-through handler on top of
// everything (it also sees drags that other handlers own).
type mouseTracker struct {
	pos     image.Point
	inside  bool
	pressed bool
}

func (u *UI) trackMouse(gtx layout.Context) {
	m := &u.mouse
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: m,
			Kinds: pointer.Move | pointer.Drag | pointer.Press | pointer.Release | pointer.Enter | pointer.Leave | pointer.Cancel})
		if !ok {
			break
		}
		e, ok := ev.(pointer.Event)
		if !ok {
			continue
		}
		m.pos = e.Position.Round()
		switch e.Kind {
		case pointer.Leave:
			m.inside = false
		case pointer.Press:
			m.pressed, m.inside = true, true
		case pointer.Release, pointer.Cancel:
			m.pressed = false
		default:
			m.inside = true
		}
	}
}

func (u *UI) registerMouse(gtx layout.Context) {
	defer clip.Rect(image.Rectangle{Max: u.size}).Push(gtx.Ops).Pop()
	defer pointer.PassOp{}.Push(gtx.Ops).Pop()
	event.Op(gtx.Ops, &u.mouse)
}

// tipAtMouse anchors a tooltip just below the pointer.
func (u *UI) tipAtMouse(key any, text string) {
	p := u.mouse.pos
	u.tip(key, text, image.Rect(p.X, p.Y, p.X+1, p.Y+dp2(16)))
}

// dp2 converts dp with the frame's metric (for helpers without a gtx).
func dp2(v float32) int { return int(v*frameMetric + 0.5) }

var frameMetric float32 = 1
