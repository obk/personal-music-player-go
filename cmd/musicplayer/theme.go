package main

import (
	"sync/atomic"

	"musicplayer/internal/win"
)

// The system theme is read once and again whenever Windows reports a setting change (not every frame).
var prefersLight atomic.Bool

func refreshSystemTheme() { prefersLight.Store(win.SystemPrefersLight()) }

func systemLight() func() bool {
	refreshSystemTheme()
	return prefersLight.Load
}
