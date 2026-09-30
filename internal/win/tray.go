//go:build windows

package win

import (
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procShellNotifyIconW      = shell32.NewProc("Shell_NotifyIconW")
	procRegisterClassExW      = user32.NewProc("RegisterClassExW")
	procCreateWindowExW       = user32.NewProc("CreateWindowExW")
	procDefWindowProcW        = user32.NewProc("DefWindowProcW")
	procDestroyWindow         = user32.NewProc("DestroyWindow")
	procGetMessageW           = user32.NewProc("GetMessageW")
	procTranslateMessage      = user32.NewProc("TranslateMessage")
	procDispatchMessageW      = user32.NewProc("DispatchMessageW")
	procPostQuitMessage       = user32.NewProc("PostQuitMessage")
	procCreatePopupMenu       = user32.NewProc("CreatePopupMenu")
	procAppendMenuW           = user32.NewProc("AppendMenuW")
	procTrackPopupMenu        = user32.NewProc("TrackPopupMenu")
	procDestroyMenu           = user32.NewProc("DestroyMenu")
	procGetCursorPos          = user32.NewProc("GetCursorPos")
	procRegisterWindowMessage = user32.NewProc("RegisterWindowMessageW")
	procGetModuleHandleW      = kernel32.NewProc("GetModuleHandleW")
)

const (
	nimAdd     = 0
	nimModify  = 1
	nimDelete  = 2
	nifMessage = 0x1
	nifIcon    = 0x2
	nifTip     = 0x4

	wmApp         = 0x8000
	wmTray        = wmApp + 1
	wmTrayTooltip = wmApp + 2
	wmTrayQuit    = wmApp + 3
	wmNull        = 0x0000
	wmLButtonUp   = 0x0202
	wmRButtonUp   = 0x0205

	mfString       = 0x0
	mfSeparator    = 0x800
	tpmReturnCmd   = 0x0100
	tpmRightButton = 0x0002
)

type notifyIconData struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         windows.GUID
	HBalloonIcon     uintptr
}

type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       uintptr
}

type msg struct {
	HWnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

// MenuItem is one entry of the tray menu; an empty Label is a separator. Action runs on the tray's thread.
type MenuItem struct {
	Label  string
	Action func()
}

// Tray is the notification-area icon with a context menu. Left click calls OnActivate. It runs its own message
// loop on a dedicated OS thread.
type Tray struct {
	hwnd       uintptr
	icon       uintptr
	menu       []MenuItem
	onActivate func()
	tip        []uint16
	created    uint32 // "TaskbarCreated": Explorer restarted, the icon must be added again
}

var trayInstance *Tray

// NewTray shows the icon. It returns nil if the notification area cannot be used.
func NewTray(icon uintptr, tooltip string, menu []MenuItem, onActivate func()) *Tray {
	t := &Tray{icon: icon, menu: menu, onActivate: onActivate}
	ready := make(chan bool)
	go t.thread(tooltip, ready)
	if !<-ready {
		return nil
	}
	return t
}

func (t *Tray) data(flags uint32) *notifyIconData {
	d := &notifyIconData{HWnd: t.hwnd, UID: 1, UFlags: flags, UCallbackMessage: wmTray, HIcon: t.icon}
	d.CbSize = uint32(unsafe.Sizeof(*d))
	copy(d.SzTip[:127], t.tip)
	return d
}

func (t *Tray) thread(tooltip string, ready chan<- bool) {
	runtime.LockOSThread()
	trayInstance = t
	inst, _, _ := procGetModuleHandleW.Call(0)
	cls, _ := windows.UTF16PtrFromString("MusicPlayerTray")
	wc := wndClassEx{LpfnWndProc: syscall.NewCallback(trayProc), HInstance: inst, LpszClassName: cls}
	wc.CbSize = uint32(unsafe.Sizeof(wc))
	procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	t.hwnd, _, _ = procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(cls)), uintptr(unsafe.Pointer(cls)), 0,
		0, 0, 0, 0, 0, 0, inst, 0)
	if t.hwnd == 0 {
		ready <- false
		return
	}
	name, _ := windows.UTF16PtrFromString("TaskbarCreated")
	r, _, _ := procRegisterWindowMessage.Call(uintptr(unsafe.Pointer(name)))
	t.created = uint32(r)
	t.tip = windows.StringToUTF16(tooltip)
	ok, _, _ := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(t.data(nifMessage|nifIcon|nifTip))))
	ready <- ok != 0
	if ok == 0 {
		procDestroyWindow.Call(t.hwnd)
		return
	}
	var m msg
	for {
		r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func trayProc(hwnd, message, wParam, lParam uintptr) uintptr {
	t := trayInstance
	switch {
	case t == nil:
	case message == wmTray:
		switch lParam & 0xffff {
		case wmLButtonUp:
			if t.onActivate != nil {
				t.onActivate()
			}
		case wmRButtonUp:
			t.showMenu()
		}
		return 0
	case message == wmTrayTooltip:
		procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(t.data(nifTip))))
		return 0
	case message == wmTrayQuit:
		procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(t.data(0))))
		procDestroyWindow.Call(hwnd)
		procPostQuitMessage.Call(0)
		return 0
	case uint32(message) == t.created && t.created != 0:
		procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(t.data(nifMessage|nifIcon|nifTip))))
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, message, wParam, lParam)
	return r
}

func (t *Tray) showMenu() {
	menu, _, _ := procCreatePopupMenu.Call()
	for i, item := range t.menu {
		if item.Label == "" {
			procAppendMenuW.Call(menu, mfSeparator, 0, 0)
			continue
		}
		label, _ := windows.UTF16PtrFromString(item.Label)
		procAppendMenuW.Call(menu, mfString, uintptr(i+1), uintptr(unsafe.Pointer(label)))
	}
	var pt struct{ X, Y int32 }
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&pt)))
	procSetForegroundWindow.Call(t.hwnd) // otherwise the menu does not close when clicking elsewhere
	cmd, _, _ := procTrackPopupMenu.Call(menu, tpmReturnCmd|tpmRightButton, uintptr(pt.X), uintptr(pt.Y), 0, t.hwnd, 0)
	procPostMessageW.Call(t.hwnd, wmNull, 0, 0)
	procDestroyMenu.Call(menu)
	if cmd > 0 && int(cmd) <= len(t.menu) && t.menu[cmd-1].Action != nil {
		t.menu[cmd-1].Action()
	}
}

// SetTooltip changes the hover text (safe from any goroutine).
func (t *Tray) SetTooltip(s string) {
	if t == nil {
		return
	}
	tip := windows.StringToUTF16(s)
	if len(tip) > 127 {
		tip = append(tip[:126], 0)
	}
	// Handed over through the tray thread, which owns t.tip.
	go func() {
		t.tip = tip
		procPostMessageW.Call(t.hwnd, wmTrayTooltip, 0, 0)
	}()
}

// Close removes the icon.
func (t *Tray) Close() {
	if t != nil {
		procSendMessageW.Call(t.hwnd, wmTrayQuit, 0, 0)
	}
}
