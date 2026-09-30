//go:build windows

package win

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"sync"
	"syscall"
	"unsafe"

	"musicplayer/internal/metadata"
)

var (
	procShowWindow               = user32.NewProc("ShowWindow")
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")
	procIsIconic                 = user32.NewProc("IsIconic")
	procSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	procSetWindowLongPtrW        = user32.NewProc("SetWindowLongPtrW")
	procCallWindowProcW          = user32.NewProc("CallWindowProcW")
	procSendMessageW             = user32.NewProc("SendMessageW")
	procPostMessageW             = user32.NewProc("PostMessageW")
	procCreateIconFromResourceEx = user32.NewProc("CreateIconFromResourceEx")
)

const (
	swHide    = 0
	swShow    = 5
	swRestore = 9

	wmSize          = 0x0005
	wmSetIcon       = 0x0080
	wmSettingChange = 0x001A
	sizeMinimized   = 1
	gwlpWndProc     = ^uintptr(3) // -4
)

// Window wraps the main (Gio) window's handle for what Gio does not offer: hiding into the tray and the icon.
type Window struct {
	hwnd    uintptr
	oldProc uintptr
	// OnMinimized is called (on the window's thread) after the window was minimized.
	OnMinimized func()
	// OnSettingChange is called (on its own goroutine) when a system setting such as the app theme changed.
	OnSettingChange func()

	drop   DropHandler
	dropOK bool // the drag in progress carries files
}

var (
	windowsMu sync.Mutex
	windowMap = map[uintptr]*Window{}
	subclass  uintptr
)

// AttachWindow subclasses hwnd to learn when it is minimized.
func AttachWindow(hwnd uintptr) *Window {
	w := &Window{hwnd: hwnd}
	windowsMu.Lock()
	windowMap[hwnd] = w
	if subclass == 0 {
		subclass = syscall.NewCallback(subclassProc)
	}
	windowsMu.Unlock()
	w.oldProc, _, _ = procSetWindowLongPtrW.Call(hwnd, gwlpWndProc, subclass)
	return w
}

func subclassProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	windowsMu.Lock()
	w := windowMap[hwnd]
	windowsMu.Unlock()
	if w == nil {
		return 0
	}
	switch msg {
	case wmEnableDrop:
		w.registerDrop()
		return 0
	case wmDropFiles:
		w.dropFiles(wParam)
		return 0
	case wmSettingChange:
		if w.OnSettingChange != nil {
			go w.OnSettingChange()
		}
	}
	r, _, _ := procCallWindowProcW.Call(w.oldProc, hwnd, msg, wParam, lParam)
	if msg == wmSize && wParam == sizeMinimized && w.OnMinimized != nil {
		go w.OnMinimized() // deferred: the state is applied after this message
	}
	return r
}

func (w *Window) Visible() bool {
	r, _, _ := procIsWindowVisible.Call(w.hwnd)
	return r != 0
}

func (w *Window) Minimized() bool {
	r, _, _ := procIsIconic.Call(w.hwnd)
	return r != 0
}

func (w *Window) Hide() { procShowWindow.Call(w.hwnd, swHide) }

// Show restores and activates the window.
func (w *Window) Show() {
	if w.Minimized() {
		procShowWindow.Call(w.hwnd, swRestore)
	} else {
		procShowWindow.Call(w.hwnd, swShow)
	}
	procSetForegroundWindow.Call(w.hwnd)
}

// Toggle hides a visible window and shows a hidden or minimized one.
func (w *Window) Toggle() {
	if w.Visible() && !w.Minimized() {
		w.Hide()
	} else {
		w.Show()
	}
}

// SetIcon sets the title-bar / taskbar icons. Posted, not sent: the window's thread may be waiting on the caller.
func (w *Window) SetIcon(small, big uintptr) {
	procPostMessageW.Call(w.hwnd, wmSetIcon, 0, small)
	procPostMessageW.Call(w.hwnd, wmSetIcon, 1, big)
}

// AppIconImage draws the application icon: an accent-coloured disc with a white play triangle.
func AppIconImage(size int) *image.RGBA {
	const ss = 4 // supersampling
	n := size * ss
	big := image.NewRGBA(image.Rect(0, 0, n, n))
	accent := color.RGBA{0x8b, 0x7b, 0xff, 0xff}
	c := float64(n) / 2
	r := c - float64(ss)
	// Triangle centred slightly right of the middle, as optical balance for the play glyph.
	ax, ay := c-0.28*r, c-0.42*r
	bx, by := c-0.28*r, c+0.42*r
	tx, ty := c+0.48*r, c
	inTri := func(x, y float64) bool {
		s := func(x1, y1, x2, y2 float64) float64 { return (x-x2)*(y1-y2) - (x1-x2)*(y-y2) }
		d1, d2, d3 := s(ax, ay, bx, by), s(bx, by, tx, ty), s(tx, ty, ax, ay)
		neg := d1 < 0 || d2 < 0 || d3 < 0
		pos := d1 > 0 || d2 > 0 || d3 > 0
		return !(neg && pos)
	}
	for y := 0; y < n; y++ {
		for x := 0; x < n; x++ {
			fx, fy := float64(x)+0.5, float64(y)+0.5
			if math.Hypot(fx-c, fy-c) > r {
				continue
			}
			if inTri(fx, fy) {
				big.SetRGBA(x, y, color.RGBA{0xff, 0xff, 0xff, 0xff})
			} else {
				big.SetRGBA(x, y, accent)
			}
		}
	}
	return metadata.Fit(big, size)
}

// NewIcon creates an HICON of the given size from the application icon image (PNG-compressed icon data).
func NewIcon(size int) uintptr {
	var buf bytes.Buffer
	if png.Encode(&buf, AppIconImage(size)) != nil {
		return 0
	}
	b := buf.Bytes()
	h, _, _ := procCreateIconFromResourceEx.Call(uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)), 1, 0x00030000,
		uintptr(size), uintptr(size), 0)
	return h
}
