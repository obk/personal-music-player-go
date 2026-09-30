//go:build windows

package win

import (
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"

	"musicplayer/internal/formats"
)

var (
	procGetOpenFileNameW = comdlg32.NewProc("GetOpenFileNameW")
	procGetSaveFileNameW = comdlg32.NewProc("GetSaveFileNameW")
)

type openFileName struct {
	LStructSize       uint32
	HwndOwner         uintptr
	HInstance         uintptr
	LpstrFilter       *uint16
	LpstrCustomFilter *uint16
	NMaxCustFilter    uint32
	NFilterIndex      uint32
	LpstrFile         *uint16
	NMaxFile          uint32
	LpstrFileTitle    *uint16
	NMaxFileTitle     uint32
	LpstrInitialDir   *uint16
	LpstrTitle        *uint16
	Flags             uint32
	NFileOffset       uint16
	NFileExtension    uint16
	LpstrDefExt       *uint16
	LCustData         uintptr
	LpfnHook          uintptr
	LpTemplateName    *uint16
	PvReserved        uintptr
	DwReserved        uint32
	FlagsEx           uint32
}

const (
	ofnOverwritePrompt  = 0x00000002
	ofnHideReadOnly     = 0x00000004
	ofnNoChangeDir      = 0x00000008
	ofnAllowMultiSelect = 0x00000200
	ofnPathMustExist    = 0x00000800
	ofnFileMustExist    = 0x00001000
	ofnExplorer         = 0x00080000
)

// filterString builds "Name (*.a *.b)\0*.a;*.b\0...\0\0".
func filterString(filters []formats.Filter) []uint16 {
	var sb strings.Builder
	for _, f := range filters {
		sb.WriteString(f.Name + " (" + strings.Join(f.Patterns, " ") + ")\x00")
		sb.WriteString(strings.Join(f.Patterns, ";") + "\x00")
	}
	sb.WriteString("\x00")
	return windows.StringToUTF16(sb.String())
}

func runDialog(owner uintptr, title, defExt string, filters []formats.Filter, flags uint32, save bool) []string {
	// Modal dialogs pump messages and use COM on the calling thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	procCoInitializeEx.Call(0, 0x2) // apartment threaded

	buf := make([]uint16, 65536)
	filter := filterString(filters)
	titleW, _ := windows.UTF16PtrFromString(title)
	ofn := openFileName{
		HwndOwner:    owner,
		LpstrFilter:  &filter[0],
		NFilterIndex: 1,
		LpstrFile:    &buf[0],
		NMaxFile:     uint32(len(buf)),
		LpstrTitle:   titleW,
		Flags:        flags | ofnExplorer | ofnNoChangeDir | ofnHideReadOnly,
	}
	if defExt != "" {
		ofn.LpstrDefExt, _ = windows.UTF16PtrFromString(defExt)
	}
	ofn.LStructSize = uint32(unsafe.Sizeof(ofn))
	proc := procGetOpenFileNameW
	if save {
		proc = procGetSaveFileNameW
	}
	if r, _, _ := proc.Call(uintptr(unsafe.Pointer(&ofn))); r == 0 {
		return nil
	}
	// Single selection: the full path. Multiple: the directory, then each name, NUL-separated, ending in NUL NUL.
	var parts []string
	start := 0
	for i := 0; i < len(buf); i++ {
		if buf[i] == 0 {
			if i == start {
				break
			}
			parts = append(parts, windows.UTF16ToString(buf[start:i]))
			start = i + 1
		}
	}
	if len(parts) <= 1 {
		return parts
	}
	dir := parts[0]
	out := make([]string, 0, len(parts)-1)
	for _, name := range parts[1:] {
		out = append(out, filepath.Join(dir, name))
	}
	return out
}

// OpenFiles shows the native open dialog (blocking; call from its own goroutine) and returns the chosen files.
func OpenFiles(owner uintptr, title string, filters []formats.Filter) []string {
	return runDialog(owner, title, "", filters, ofnAllowMultiSelect|ofnFileMustExist|ofnPathMustExist, false)
}

// SaveFile shows the native save dialog (blocking) and returns the chosen path, or "".
func SaveFile(owner uintptr, title, defExt string, filters []formats.Filter) string {
	if p := runDialog(owner, title, defExt, filters, ofnOverwritePrompt|ofnPathMustExist, true); len(p) > 0 {
		return p[0]
	}
	return ""
}

var (
	procCoCreateInstance = ole32.NewProc("CoCreateInstance")
	procCoTaskMemFree    = ole32.NewProc("CoTaskMemFree")
	clsidFileOpenDialog  = guid("{DC1C5A9C-E88A-4dde-A5A1-60F82A20AEF7}")
	iidIFileOpenDialog   = guid("{d57c7288-d4ad-4768-be02-9d969532d960}")
)

// PickFolder shows the modern folder picker (blocking; call from its own goroutine) and returns the folder, or "".
func PickFolder(owner uintptr, title string) string {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	procCoInitializeEx.Call(0, 0x2) // apartment threaded
	var dlg unsafe.Pointer
	if r, _, _ := procCoCreateInstance.Call(uintptr(unsafe.Pointer(&clsidFileOpenDialog)), 0, 1,
		uintptr(unsafe.Pointer(&iidIFileOpenDialog)), uintptr(unsafe.Pointer(&dlg))); failed(r) || dlg == nil {
		return ""
	}
	defer release(dlg)
	var opts uint32
	vcall(dlg, 10, uintptr(unsafe.Pointer(&opts))) // GetOptions
	vcall(dlg, 9, uintptr(opts|0x20|0x40|0x800))   // SetOptions: FOS_PICKFOLDERS | FOS_FORCEFILESYSTEM | FOS_PATHMUSTEXIST
	if t, err := windows.UTF16PtrFromString(title); err == nil {
		vcall(dlg, 17, uintptr(unsafe.Pointer(t))) // SetTitle
	}
	if failed(vcall(dlg, 3, owner)) { // Show (cancelled: an error HRESULT)
		return ""
	}
	var item unsafe.Pointer
	if failed(vcall(dlg, 20, uintptr(unsafe.Pointer(&item)))) || item == nil { // GetResult
		return ""
	}
	defer release(item)
	var name *uint16
	if failed(vcall(item, 5, 0x80058000, uintptr(unsafe.Pointer(&name)))) || name == nil { // GetDisplayName(SIGDN_FILESYSPATH)
		return ""
	}
	defer procCoTaskMemFree.Call(uintptr(unsafe.Pointer(name)))
	return windows.UTF16PtrToString(name)
}
