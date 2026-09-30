package ui

import (
	"image"
	"strconv"
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
	"musicplayer/internal/metadata"
)

type dialogField struct {
	label   string
	editor  widget.Editor
	numeric bool
	focus   fade
}

// dialogState is the one modal dialog: a name prompt, a confirmation, or the metadata editor.
type dialogState struct {
	open     bool
	title    string
	message  string
	okLabel  string
	danger   bool
	width    float32
	fields   []*dialogField
	columns  int // numeric fields share one row
	onOK     func(values []string)
	ok       button
	cancel   button
	frames   int
	scrim    int
	errorMsg string
}

func (d *dialogState) close() { d.open = false }

func (d *dialogState) hasTextFocus(gtx layout.Context) bool {
	if !d.open {
		return false
	}
	for _, f := range d.fields {
		if gtx.Focused(&f.editor) {
			return true
		}
	}
	return false
}

func (u *UI) openDialog(d dialogState) {
	d.open = true
	u.dialog = d
	u.menu.open = false
}

// openNameDialog asks for a name (new playlist, rename).
func (u *UI) openNameDialog(title, okLabel, initial string, onOK func(name string)) {
	f := &dialogField{label: "Name"}
	f.editor.SetText(initial)
	f.editor.SetCaret(len([]rune(initial)), 0)
	u.openDialog(dialogState{title: title, okLabel: okLabel, width: 400, fields: []*dialogField{f},
		onOK: func(v []string) {
			if strings.TrimSpace(v[0]) != "" {
				onOK(strings.TrimSpace(v[0]))
			}
		}})
}

// openConfirm asks before a destructive action.
func (u *UI) openConfirm(title, message, okLabel string, onOK func()) {
	u.openDialog(dialogState{title: title, message: message, okLabel: okLabel, danger: true, width: 400,
		onOK: func([]string) { onOK() }})
}

// openTagEditor edits a track's metadata.
func (u *UI) openTagEditor(t *library.Track) {
	tf := metadata.FieldsOf(t.Info)
	num := func(v int) string {
		if v <= 0 {
			return ""
		}
		return strconv.Itoa(v)
	}
	values := []struct {
		label, value string
		numeric      bool
	}{
		{"Title", tf.Title, false}, {"Artist", tf.Artist, false}, {"Album", tf.Album, false},
		{"Album artist", tf.AlbumArtist, false}, {"Genre", tf.Genre, false},
		{"Year", num(tf.Year), true}, {"Track", num(tf.TrackNumber), true}, {"Disc", num(tf.DiscNumber), true},
	}
	var fields []*dialogField
	for _, v := range values {
		f := &dialogField{label: v.label, numeric: v.numeric}
		f.editor.SetText(v.value)
		fields = append(fields, f)
	}
	id := t.ID
	u.openDialog(dialogState{title: "Edit metadata", message: t.Path, okLabel: "Save", width: 560, fields: fields,
		columns: 3, onOK: func(v []string) {
			atoi := func(s string) int { n, _ := strconv.Atoi(strings.TrimSpace(s)); return n }
			u.p.EditTags(id, metadata.TagFields{Title: strings.TrimSpace(v[0]), Artist: strings.TrimSpace(v[1]),
				Album: strings.TrimSpace(v[2]), AlbumArtist: strings.TrimSpace(v[3]), Genre: strings.TrimSpace(v[4]),
				Year: atoi(v[5]), TrackNumber: atoi(v[6]), DiscNumber: atoi(v[7])})
		}})
}

func (u *UI) submitDialog() {
	d := &u.dialog
	var values []string
	for _, f := range d.fields {
		if f.numeric {
			if s := strings.TrimSpace(f.editor.Text()); s != "" {
				if n, err := strconv.Atoi(s); err != nil || n < 0 {
					d.errorMsg = f.label + " must be a whole number"
					return
				}
			}
		}
		values = append(values, f.editor.Text())
	}
	d.open = false
	if d.onOK != nil {
		fn := d.onOK
		u.later(func() { fn(values) })
	}
}

func (u *UI) layoutDialog(gtx layout.Context, g geometry) {
	d := &u.dialog
	if !d.open {
		return
	}
	d.frames++
	// Scrim: swallows all input behind the dialog.
	fillRect(gtx.Ops, image.Rectangle{Max: g.ws}, pal.scrim)
	func() {
		defer clip.Rect(image.Rectangle{Max: g.ws}).Push(gtx.Ops).Pop()
		event.Op(gtx.Ops, &d.scrim)
		for {
			if _, ok := gtx.Event(pointer.Filter{Target: &d.scrim, Kinds: pointer.Press}); !ok {
				break
			}
		}
	}()
	if d.frames == 1 && len(d.fields) > 0 {
		gtx.Execute(key.FocusCmd{Tag: &d.fields[0].editor})
	}
	for _, f := range d.fields {
		for {
			ev, ok := f.editor.Update(gtx)
			if !ok {
				break
			}
			if _, ok := ev.(widget.SubmitEvent); ok {
				u.submitDialog()
			}
		}
	}
	if d.cancel.click.Clicked(gtx) {
		d.open = false
		return
	}

	w := min(dp(gtx, d.width), g.ws.X-dp(gtx, 32))
	pad := dp(gtx, 24)
	inner := w - 2*pad
	cg := gtx
	cg.Constraints = layout.Constraints{Max: image.Pt(inner, g.ws.Y)}
	content, cd := record(cg, func(gtx layout.Context) layout.Dimensions {
		y := 0
		c, dd := u.text(gtx, d.title, textStyle{size: 18, weight: font.SemiBold, color: pal.text1})
		place(gtx.Ops, image.Pt(0, y), c)
		y += dd.Size.Y + dp(gtx, 8)
		if d.message != "" {
			mc, md := record(gtx, func(gtx layout.Context) layout.Dimensions {
				gtx.Constraints.Min = image.Point{}
				return u.label(gtx, d.message, textStyle{size: fsCell, color: pal.text2, maxLines: 3})
			})
			place(gtx.Ops, image.Pt(0, y), mc)
			y += md.Size.Y + dp(gtx, 16)
		} else {
			y += dp(gtx, 8)
		}
		numCol := 0
		for i, f := range d.fields {
			fw := inner
			x := 0
			if f.numeric && d.columns > 0 {
				gap := dp(gtx, 12)
				fw = (inner - gap*(d.columns-1)) / d.columns
				x = numCol * (fw + gap)
				numCol++
			}
			fy := y
			lc, ld := u.text(gtx, f.label, textStyle{size: fsCaption, weight: font.Medium, color: pal.text2})
			place(gtx.Ops, image.Pt(x, fy), lc)
			fy += ld.Size.Y + dp(gtx, 6)
			u.textInput(gtx, f, image.Rect(x, fy, x+fw, fy+dp(gtx, 36)))
			fy += dp(gtx, 36) + dp(gtx, 14)
			last := i == len(d.fields)-1
			if !f.numeric || numCol == d.columns || last || !d.fields[i+1].numeric {
				y = fy
				numCol = 0
			}
		}
		if d.errorMsg != "" {
			c, dd := u.text(gtx, d.errorMsg, textStyle{size: fsCaption, color: pal.danger})
			place(gtx.Ops, image.Pt(0, y), c)
			y += dd.Size.Y + dp(gtx, 12)
		}
		// Buttons, right-aligned.
		y += dp(gtx, 8)
		okKind := btnPrimary
		if d.danger {
			okKind = btnDanger
		}
		oc, od := record(gtx, func(gtx layout.Context) layout.Dimensions {
			dd, clicked := d.ok.layout(u, gtx, d.okLabel, "", okKind, 36)
			if clicked {
				u.later(u.submitDialog)
			}
			return dd
		})
		ox := inner - od.Size.X
		place(gtx.Ops, image.Pt(ox, y), oc)
		cc, cdim := record(gtx, func(gtx layout.Context) layout.Dimensions {
			dd, _ := d.cancel.layout(u, gtx, "Cancel", "", btnSecondary, 36)
			return dd
		})
		place(gtx.Ops, image.Pt(ox-dp(gtx, 8)-cdim.Size.X, y), cc)
		y += dp(gtx, 36)
		return layout.Dimensions{Size: image.Pt(inner, y)}
	})
	h := cd.Size.Y + 2*pad
	r := image.Rect((g.ws.X-w)/2, (g.ws.Y-h)/2, (g.ws.X+w)/2, (g.ws.Y+h)/2)
	shadow(gtx.Ops, r, dp(gtx, rPanel), dp(gtx, 24), dp(gtx, 8))
	fillRRect(gtx.Ops, r, dp(gtx, rPanel), pal.elevated)
	if pal.light {
		strokeRRect(gtx.Ops, r, dp(gtx, rPanel), 1, pal.divider)
	}
	func() {
		defer u.push(gtx, r.Min.Add(image.Pt(pad, pad)))()
		content.Add(gtx.Ops)
	}()
}

// textInput draws an editor in a 36 dp input box (4 dp radius, accent outline when focused).
func (u *UI) textInput(gtx layout.Context, f *dialogField, r image.Rectangle) {
	focused := gtx.Focused(&f.editor)
	bg := pal.hover
	if pal.light {
		bg = pal.bg
	}
	rad := dp(gtx, rSmall)
	fillRRect(gtx.Ops, r, rad, bg)
	if a := f.focus.to(boolf(focused), fast); a > 0 {
		strokeRRect(gtx.Ops, r, rad, float32(dp(gtx, 1)), withAlpha(pal.accent, a))
	} else {
		strokeRRect(gtx.Ops, r, rad, 1, pal.divider)
	}
	f.editor.SingleLine, f.editor.Submit = true, true
	px := dp(gtx, 10)
	u.at(gtx, image.Pt(r.Min.X+px, r.Min.Y), image.Pt(r.Dx()-2*px, r.Dy()), func(gtx layout.Context) layout.Dimensions {
		c, d := record(gtx, func(gtx layout.Context) layout.Dimensions {
			return f.editor.Layout(gtx, u.shaper, uiFont(font.Normal), unit.Sp(fsBody), colorMaterial(gtx.Ops, pal.text1),
				colorMaterial(gtx.Ops, withAlpha(pal.accent, 0.35)))
		})
		place(gtx.Ops, image.Pt(0, (r.Dy()-d.Size.Y)/2), c)
		pointerCursor(gtx, image.Rect(0, 0, r.Dx()-2*px, r.Dy()), pointer.CursorText)
		return layout.Dimensions{Size: image.Pt(r.Dx()-2*px, r.Dy())}
	})
}
