//go:build windows

package win

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procOleInitialize     = ole32.NewProc("OleInitialize")
	procRegisterDragDrop  = ole32.NewProc("RegisterDragDrop")
	procReleaseStgMedium  = ole32.NewProc("ReleaseStgMedium")
	procDragAcceptFiles   = shell32.NewProc("DragAcceptFiles")
	procDragQueryFileW    = shell32.NewProc("DragQueryFileW")
	procDragQueryPoint    = shell32.NewProc("DragQueryPoint")
	procDragFinish        = shell32.NewProc("DragFinish")
	procScreenToClient    = user32.NewProc("ScreenToClient")
	iidIDropTarget        = guid("{00000122-0000-0000-C000-000000000046}")
	dropTargets           = map[uintptr]*Window{} // COM object address -> window
	dropTargetVtbl        unsafe.Pointer
	errAlreadyInitialized = uintptr(1) // S_FALSE from OleInitialize
)

const (
	wmEnableDrop    = wmApp + 20
	wmDropFiles     = 0x0233
	cfHDrop         = 15
	tymedHGlobal    = 1
	dvaspectContent = 1
	dropEffectNone  = 0
	dropEffectCopy  = 1
)

// DropHandler receives files dragged in from Explorer. Coordinates are client-area pixels. Both callbacks run on
// the window's thread and must return quickly.
type DropHandler struct {
	Over func(x, y int, active bool) // pointer moving over the window with files; active=false when it leaves
	Drop func(paths []string, x, y int)
}

type formatEtc struct {
	CfFormat uint16
	Ptd      uintptr
	Aspect   uint32
	Lindex   int32
	Tymed    uint32
}

type stgMedium struct {
	Tymed          uint32
	Handle         uintptr
	PUnkForRelease uintptr
}

var hdropFormat = formatEtc{CfFormat: cfHDrop, Aspect: dvaspectContent, Lindex: -1, Tymed: tymedHGlobal}

// EnableDrop registers the window as a drop target: OLE drag and drop (with hover feedback) when possible, else
// the classic WM_DROPFILES (drop only). Registration happens on the window's own thread.
func (w *Window) EnableDrop(h DropHandler) {
	w.drop = h
	procPostMessageW.Call(w.hwnd, wmEnableDrop, 0, 0)
}

// registerDrop runs on the window's thread.
func (w *Window) registerDrop() {
	r, _, _ := procOleInitialize.Call(0)
	if r == 0 || r == errAlreadyInitialized {
		obj := newDropTarget(w)
		if hr, _, _ := procRegisterDragDrop.Call(w.hwnd, uintptr(obj)); !failed(hr) {
			return
		}
	}
	procDragAcceptFiles.Call(w.hwnd, 1)
}

func newDropTarget(w *Window) unsafe.Pointer {
	if dropTargetVtbl == nil {
		qi := syscall.NewCallback(func(this, riid, ppv uintptr) uintptr {
			iid := *(*windows.GUID)(ptr(riid))
			if iid != iidIUnknown && iid != iidIDropTarget {
				*(*uintptr)(ptr(ppv)) = 0
				return 0x80004002 // E_NOINTERFACE
			}
			*(*uintptr)(ptr(ppv)) = this
			return 0
		})
		ref := syscall.NewCallback(func(this uintptr) uintptr { return 1 }) // lives as long as the window
		enter := syscall.NewCallback(func(this, data, keys, pt, effect uintptr) uintptr {
			t := dropTargets[this]
			t.dropOK = hasFiles(ptr(data))
			t.dragOver(pt, effect)
			return 0
		})
		over := syscall.NewCallback(func(this, keys, pt, effect uintptr) uintptr {
			dropTargets[this].dragOver(pt, effect)
			return 0
		})
		leave := syscall.NewCallback(func(this uintptr) uintptr {
			t := dropTargets[this]
			if t.drop.Over != nil {
				t.drop.Over(0, 0, false)
			}
			return 0
		})
		drop := syscall.NewCallback(func(this, data, keys, pt, effect uintptr) uintptr {
			t := dropTargets[this]
			x, y := t.clientPoint(pt)
			if t.drop.Over != nil {
				t.drop.Over(0, 0, false)
			}
			paths := filesOf(ptr(data))
			*(*uint32)(ptr(effect)) = dropEffectNone
			if len(paths) > 0 {
				*(*uint32)(ptr(effect)) = dropEffectCopy
				if t.drop.Drop != nil {
					t.drop.Drop(paths, x, y)
				}
			}
			return 0
		})
		mem, _ := windows.LocalAlloc(windows.LPTR, 7*8)
		tbl := unsafe.Slice((*uintptr)(ptr(mem)), 7)
		tbl[0], tbl[1], tbl[2], tbl[3], tbl[4], tbl[5], tbl[6] = qi, ref, ref, enter, over, leave, drop
		dropTargetVtbl = ptr(mem)
	}
	mem, _ := windows.LocalAlloc(windows.LPTR, 8)
	*(*unsafe.Pointer)(ptr(mem)) = dropTargetVtbl
	dropTargets[mem] = w
	return ptr(mem)
}

// clientPoint unpacks a POINTL passed by value (screen coordinates) into client coordinates.
func (w *Window) clientPoint(pt uintptr) (int, int) {
	p := struct{ X, Y int32 }{int32(uint32(pt)), int32(uint32(pt >> 32))}
	procScreenToClient.Call(w.hwnd, uintptr(unsafe.Pointer(&p)))
	return int(p.X), int(p.Y)
}

func (w *Window) dragOver(pt, effect uintptr) {
	if w.dropOK {
		*(*uint32)(ptr(effect)) = dropEffectCopy
	} else {
		*(*uint32)(ptr(effect)) = dropEffectNone
	}
	if w.dropOK && w.drop.Over != nil {
		x, y := w.clientPoint(pt)
		w.drop.Over(x, y, true)
	}
}

func hasFiles(data unsafe.Pointer) bool {
	return vcall(data, 5, uintptr(unsafe.Pointer(&hdropFormat))) == 0 // IDataObject::QueryGetData
}

func filesOf(data unsafe.Pointer) []string {
	var m stgMedium
	if failed(vcall(data, 3, uintptr(unsafe.Pointer(&hdropFormat)), uintptr(unsafe.Pointer(&m)))) { // GetData
		return nil
	}
	defer procReleaseStgMedium.Call(uintptr(unsafe.Pointer(&m)))
	return hdropPaths(m.Handle)
}

func hdropPaths(h uintptr) []string {
	n, _, _ := procDragQueryFileW.Call(h, 0xFFFFFFFF, 0, 0)
	var out []string
	for i := uintptr(0); i < n; i++ {
		size, _, _ := procDragQueryFileW.Call(h, i, 0, 0)
		buf := make([]uint16, size+1)
		procDragQueryFileW.Call(h, i, uintptr(unsafe.Pointer(&buf[0])), size+1)
		out = append(out, windows.UTF16ToString(buf))
	}
	return out
}

// dropFiles handles the classic WM_DROPFILES (when OLE registration was not possible).
func (w *Window) dropFiles(hdrop uintptr) {
	var p struct{ X, Y int32 }
	procDragQueryPoint.Call(hdrop, uintptr(unsafe.Pointer(&p)))
	paths := hdropPaths(hdrop)
	procDragFinish.Call(hdrop)
	if len(paths) > 0 && w.drop.Drop != nil {
		w.drop.Drop(paths, int(p.X), int(p.Y))
	}
}
