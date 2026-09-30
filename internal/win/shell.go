//go:build windows

package win

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows/registry"
)

// ShowInExplorer opens File Explorer with path selected.
func ShowInExplorer(path string) {
	cmd := exec.Command("explorer.exe")
	// explorer parses its own command line: the quotes must surround the path only.
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `explorer.exe /select,"` + path + `"`}
	_ = cmd.Start()
}

// SystemPrefersLight reports whether Windows is set to the light app theme.
func SystemPrefersLight() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("AppsUseLightTheme")
	return err == nil && v != 0
}
