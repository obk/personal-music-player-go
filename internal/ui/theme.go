// Package ui is the Gio user interface: a three-pane desktop layout (sidebar, main view, optional queue drawer)
// above a full-width playback bar, in a dark or light theme.
package ui

import (
	"image"
	"image/color"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/font/opentype"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"

	"musicplayer/assets"
)

// argb converts 0xAARRGGBB.
func argb(v uint32) color.NRGBA {
	return color.NRGBA{A: uint8(v >> 24), R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v)}
}

func rgb(v uint32) color.NRGBA { return argb(0xFF000000 | v) }

// palette holds the colour tokens of one theme.
type palette struct {
	light                              bool
	bg, surface, elevated              color.NRGBA
	hover, selected, divider           color.NRGBA
	text1, text2, text3                color.NRGBA
	accent, accentHover, accentPressed color.NRGBA
	onAccent, danger                   color.NRGBA
	shadow                             color.NRGBA // menu / dialog shadow
	scrim                              color.NRGBA // behind dialogs
	artPairs                           [8][2]color.NRGBA
	artIcon                            color.NRGBA
}

var darkPalette = palette{
	bg: rgb(0x121212), surface: rgb(0x181818), elevated: rgb(0x242424),
	hover: rgb(0x2A2A2A), selected: rgb(0x30343A), divider: rgb(0x2E2E2E),
	text1: rgb(0xF1F1F1), text2: rgb(0xA0A0A0), text3: rgb(0x6B6B6B),
	accent: rgb(0x00E5FF), accentHover: rgb(0x5CEFFF), accentPressed: rgb(0x00B8CC),
	onAccent: rgb(0x00262B), danger: rgb(0xFF5470),
	shadow: argb(0x80000000), scrim: argb(0x99000000),
	artPairs: [8][2]color.NRGBA{
		{rgb(0x2B3A55), rgb(0x1A2233)}, {rgb(0x3D2B55), rgb(0x221A33)}, {rgb(0x2B554A), rgb(0x1A332C)},
		{rgb(0x553A2B), rgb(0x33221A)}, {rgb(0x552B3A), rgb(0x331A22)}, {rgb(0x3A552B), rgb(0x22331A)},
		{rgb(0x2B4F55), rgb(0x1A2F33)}, {rgb(0x4A4A4A), rgb(0x262626)},
	},
	artIcon: argb(0x8CFFFFFF),
}

var lightPalette = palette{
	light: true,
	bg:    rgb(0xF7F7F8), surface: rgb(0xFFFFFF), elevated: rgb(0xFFFFFF),
	hover: rgb(0xEDEDF0), selected: rgb(0xDDF6F9), divider: rgb(0xE2E2E6),
	text1: rgb(0x16161A), text2: rgb(0x5C5C66), text3: rgb(0x9A9AA3),
	accent: rgb(0x0097A7), accentHover: rgb(0x00838F), accentPressed: rgb(0x006F7A),
	onAccent: rgb(0xFFFFFF), danger: rgb(0xD6334F),
	shadow: argb(0x24000000), scrim: argb(0x66000000),
	artPairs: [8][2]color.NRGBA{
		{rgb(0xDCE3F0), rgb(0xC5CEDF)}, {rgb(0xE6DCF0), rgb(0xD3C5DF)}, {rgb(0xDCF0EA), rgb(0xC5DFD6)},
		{rgb(0xF0E3DC), rgb(0xDFCEC5)}, {rgb(0xF0DCE3), rgb(0xDFC5CE)}, {rgb(0xE3F0DC), rgb(0xCEDFC5)},
		{rgb(0xDCEDF0), rgb(0xC5DBDF)}, {rgb(0xE6E6E6), rgb(0xD0D0D0)},
	},
	artIcon: argb(0x6616161A),
}

// pal is the palette of the current frame.
var pal = &darkPalette

// Corner radii (dp).
const (
	rSmall = 4 // buttons, inputs, menu items, rows, art <= 64 px
	rArt   = 6 // art > 64 px
	rPanel = 8 // menus, tooltips, dialogs, drawer, drop overlay
)

// Type scale (sp).
const (
	fsPageHeader = 28
	fsSection    = 16
	fsBody       = 14
	fsCell       = 13
	fsCaption    = 12
	fsColumn     = 11
	fsTime       = 11
)

// Material Icons codepoints.
const (
	icPlay       = ""
	icPause      = ""
	icNext       = ""
	icPrev       = ""
	icShuffle    = ""
	icRepeat     = ""
	icRepeatOne  = ""
	icVolumeUp   = ""
	icVolumeDown = ""
	icVolumeOff  = ""
	icClose      = ""
	icMinimize   = ""
	icMaximize   = ""
	icRestore    = ""
	icNote       = ""
	icSpeaker    = ""
	icCheck      = ""
	icTune       = ""
	icSearch     = ""
	icSongs      = "" // library_music
	icAlbum      = ""
	icArtist     = ""
	icFolder     = ""
	icPlaylist   = "" // queue_music
	icQueue      = "" // playlist_play
	icAdd        = ""
	icHeart      = ""
	icHeartLine  = ""
	icMore       = ""
	icBack       = ""
	icLight      = ""
	icDark       = ""
	icAuto       = "" // brightness_auto
	icNowPlaying = "" // graphic_eq
	icPlayNext   = "" // queue_play_next
	icAddQueue   = "" // playlist_add
	icExplorer   = "" // folder_open
	icEdit       = ""
	icDelete     = ""
	icChevron    = ""
	icAddFolder  = "" // create_new_folder
	icExport     = "" // save_alt
	icUndo       = ""
)

const (
	interFace = "Inter"
)

var iconFont font.Font

// newShaper loads the bundled Inter weights and the icon font. System fonts stay available as a fallback for
// scripts Inter lacks (CJK lyrics, ...).
func newShaper() *text.Shaper {
	var coll []font.FontFace
	for _, f := range []struct {
		data   []byte
		weight font.Weight
	}{
		{assets.InterRegular, font.Normal}, {assets.InterMedium, font.Medium},
		{assets.InterSemiBold, font.SemiBold}, {assets.InterBold, font.Bold},
	} {
		face, err := opentype.Parse(f.data)
		if err != nil {
			continue
		}
		coll = append(coll, font.FontFace{Font: font.Font{Typeface: interFace, Weight: f.weight}, Face: face})
	}
	if face, err := opentype.Parse(assets.IconFont); err == nil {
		iconFont = face.Font()
		coll = append(coll, font.FontFace{Font: iconFont, Face: face})
	}
	return text.NewShaper(text.WithCollection(coll))
}

func uiFont(w font.Weight) font.Font {
	return font.Font{Typeface: interFace + ", Segoe UI Variable, Segoe UI, Noto Sans", Weight: w}
}

// withAlpha scales c's alpha by a (0..1).
func withAlpha(c color.NRGBA, a float32) color.NRGBA {
	c.A = uint8(float32(c.A)*clamp01(a) + 0.5)
	return c
}

func mixColor(a, b color.NRGBA, t float32) color.NRGBA {
	t = clamp01(t)
	l := func(x, y uint8) uint8 { return uint8(float32(x) + (float32(y)-float32(x))*t + 0.5) }
	return color.NRGBA{l(a.R, b.R), l(a.G, b.G), l(a.B, b.B), l(a.A, b.A)}
}

func clamp01(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func boolf(b bool) float32 {
	if b {
		return 1
	}
	return 0
}

// ---- drawing primitives ----

func fillRect(ops *op.Ops, r image.Rectangle, c color.NRGBA) {
	if c.A == 0 || r.Empty() {
		return
	}
	paint.FillShape(ops, c, clip.Rect(r).Op())
}

func fillRRect(ops *op.Ops, r image.Rectangle, radius int, c color.NRGBA) {
	if c.A == 0 || r.Empty() {
		return
	}
	radius = min(radius, r.Dx()/2, r.Dy()/2)
	paint.FillShape(ops, c, clip.UniformRRect(r, radius).Op(ops))
}

func strokeRRect(ops *op.Ops, r image.Rectangle, radius int, width float32, c color.NRGBA) {
	if c.A == 0 || r.Empty() {
		return
	}
	radius = min(radius, r.Dx()/2, r.Dy()/2)
	paint.FillShape(ops, c, clip.Stroke{Path: clip.UniformRRect(r, radius).Path(ops), Width: width}.Op())
}

func fillCircle(ops *op.Ops, center image.Point, radius int, c color.NRGBA) {
	r := image.Rect(center.X-radius, center.Y-radius, center.X+radius, center.Y+radius)
	paint.FillShape(ops, c, clip.Ellipse(r).Op(ops))
}

// shadow approximates a soft drop shadow with stacked translucent rounded rectangles (8px down, 24px blur).
func shadow(ops *op.Ops, r image.Rectangle, radius, blur, offsetY int) {
	const layers = 8
	r = r.Add(image.Pt(0, offsetY))
	c := pal.shadow
	for i := layers; i >= 1; i-- {
		grow := blur * i / layers / 2
		a := 1.8 / layers * (1 - float32(i-1)/layers)
		fillRRect(ops, r.Inset(-grow), radius+grow, withAlpha(c, a))
	}
}

// hgradient paints r with a horizontal gradient from c1 (left) to c2 (right).
func hgradient(ops *op.Ops, r image.Rectangle, c1, c2 color.NRGBA) {
	defer clip.Rect(r).Push(ops).Pop()
	paint.LinearGradientOp{Stop1: f32.Pt(float32(r.Min.X), 0), Color1: c1,
		Stop2: f32.Pt(float32(r.Max.X), 0), Color2: c2}.Add(ops)
	paint.PaintOp{}.Add(ops)
}

// colorMaterial records a paint.ColorOp for text drawing.
func colorMaterial(ops *op.Ops, c color.NRGBA) op.CallOp {
	m := op.Record(ops)
	paint.ColorOp{Color: c}.Add(ops)
	return m.Stop()
}
