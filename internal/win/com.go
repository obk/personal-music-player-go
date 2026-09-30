//go:build windows

// Package win holds the Windows integration: media transport controls (SMTC), the tray icon, native file
// dialogs and window helpers. COM / WinRT is called through raw vtables.
package win

import (
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	combase  = windows.NewLazySystemDLL("combase.dll")
	shlwapi  = windows.NewLazySystemDLL("shlwapi.dll")
	shcore   = windows.NewLazySystemDLL("shcore.dll")
	user32   = windows.NewLazySystemDLL("user32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	comdlg32 = windows.NewLazySystemDLL("comdlg32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	ole32    = windows.NewLazySystemDLL("ole32.dll")

	procRoInitialize                       = combase.NewProc("RoInitialize")
	procRoGetActivationFactory             = combase.NewProc("RoGetActivationFactory")
	procRoActivateInstance                 = combase.NewProc("RoActivateInstance")
	procWindowsCreateString                = combase.NewProc("WindowsCreateString")
	procWindowsDeleteString                = combase.NewProc("WindowsDeleteString")
	procSHCreateMemStream                  = shlwapi.NewProc("SHCreateMemStream")
	procCreateRandomAccessStreamOverStream = shcore.NewProc("CreateRandomAccessStreamOverStream")
	procCoInitializeEx                     = ole32.NewProc("CoInitializeEx")
)

func guid(s string) windows.GUID {
	g, err := windows.GUIDFromString(s)
	if err != nil {
		panic(err)
	}
	return g
}

var (
	iidIUnknown     = guid("{00000000-0000-0000-C000-000000000046}")
	iidIAgileObject = guid("{94ea2b94-e9cc-49e0-c0ff-ee64ca8f5b90}")
)

// ptr turns an address received from Windows into a pointer (without the vet check for arbitrary conversions;
// every address passed here is a live COM object or buffer owned by the callee's contract).
func ptr(u uintptr) unsafe.Pointer { return *(*unsafe.Pointer)(unsafe.Pointer(&u)) }

// vcall calls method idx of a COM object's vtable (index 0 = QueryInterface) and returns the HRESULT.
func vcall(obj unsafe.Pointer, idx int, args ...uintptr) uintptr {
	vtbl := *(*unsafe.Pointer)(obj)
	fn := *(*uintptr)(unsafe.Add(vtbl, uintptr(idx)*unsafe.Sizeof(uintptr(0))))
	r, _, _ := syscall.SyscallN(fn, append([]uintptr{uintptr(obj)}, args...)...)
	return r
}

func failed(hr uintptr) bool { return int32(hr) < 0 }

func release(obj unsafe.Pointer) {
	if obj != nil {
		vcall(obj, 2)
	}
}

func queryInterface(obj unsafe.Pointer, iid *windows.GUID) unsafe.Pointer {
	var out unsafe.Pointer
	if failed(vcall(obj, 0, uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&out)))) {
		return nil
	}
	return out
}

// hstring is a WinRT string; free with deleteHString.
type hstring uintptr

func newHString(s string) hstring {
	u := windows.StringToUTF16(s)
	var h hstring
	if len(u) <= 1 {
		return 0 // the empty string is the null HSTRING
	}
	procWindowsCreateString.Call(uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1), uintptr(unsafe.Pointer(&h)))
	return h
}

func (h hstring) free() {
	if h != 0 {
		procWindowsDeleteString.Call(uintptr(h))
	}
}

func activationFactory(class string, iid *windows.GUID) unsafe.Pointer {
	h := newHString(class)
	defer h.free()
	var out unsafe.Pointer
	r, _, _ := procRoGetActivationFactory.Call(uintptr(h), uintptr(unsafe.Pointer(iid)), uintptr(unsafe.Pointer(&out)))
	if failed(r) {
		return nil
	}
	return out
}

func activateInstance(class string) unsafe.Pointer {
	h := newHString(class)
	defer h.free()
	var out unsafe.Pointer
	r, _, _ := procRoActivateInstance.Call(uintptr(h), uintptr(unsafe.Pointer(&out)))
	if failed(r) {
		return nil
	}
	return out
}

// delegate is a minimal COM object implementing a WinRT event handler (IUnknown + Invoke(sender, args)). Its
// memory is allocated outside the Go heap because Windows keeps and calls it from its own threads.
type delegate struct {
	obj    unsafe.Pointer // [vtbl pointer] in LocalAlloc'ed memory
	iid    windows.GUID
	refs   int32
	invoke func(sender, args unsafe.Pointer)
}

var (
	delegates    = map[uintptr]*delegate{}
	delegateVtbl unsafe.Pointer
)

func delegateFor(this uintptr) *delegate { return delegates[this] }

func initDelegateVtbl() {
	if delegateVtbl != nil {
		return
	}
	qi := syscall.NewCallback(func(this, riid, ppv uintptr) uintptr {
		d := delegateFor(this)
		iid := *(*windows.GUID)(ptr(riid))
		if d == nil || (iid != iidIUnknown && iid != iidIAgileObject && iid != d.iid) {
			*(*uintptr)(ptr(ppv)) = 0
			return 0x80004002 // E_NOINTERFACE
		}
		*(*uintptr)(ptr(ppv)) = this
		return 0
	})
	addRef := syscall.NewCallback(func(this uintptr) uintptr { return 1 })
	rel := syscall.NewCallback(func(this uintptr) uintptr { return 1 }) // lives as long as the process
	invoke := syscall.NewCallback(func(this, sender, args uintptr) uintptr {
		if d := delegateFor(this); d != nil {
			d.invoke(ptr(sender), ptr(args))
		}
		return 0
	})
	mem, _ := windows.LocalAlloc(windows.LPTR, 4*8)
	tbl := unsafe.Slice((*uintptr)(ptr(mem)), 4)
	tbl[0], tbl[1], tbl[2], tbl[3] = qi, addRef, rel, invoke
	delegateVtbl = ptr(mem)
}

// newDelegate creates an event handler object for the delegate interface iid. Must be called before the object
// can be invoked (the map is written only here, before registration).
func newDelegate(iid windows.GUID, invoke func(sender, args unsafe.Pointer)) *delegate {
	initDelegateVtbl()
	mem, _ := windows.LocalAlloc(windows.LPTR, 8)
	*(*unsafe.Pointer)(ptr(mem)) = delegateVtbl
	d := &delegate{obj: ptr(mem), iid: iid, invoke: invoke}
	delegates[mem] = d
	return d
}
