// Command musicplayer is a Windows desktop music player: FFmpeg decoding, miniaudio output (with WASAPI exclusive
// mode), a music library with playlists, a 10-band equalizer, synchronised lyrics and a Gio user interface.
package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"

	"gioui.org/app"
	"gioui.org/io/event"
	"gioui.org/io/system"
	"gioui.org/unit"

	"musicplayer/internal/audio"
	"musicplayer/internal/core"
	"musicplayer/internal/formats"
	"musicplayer/internal/library"
	"musicplayer/internal/player"
	"musicplayer/internal/settings"
	"musicplayer/internal/ui"
	"musicplayer/internal/win"
)

func main() {
	settingsFile := settings.DefaultPath()
	store := settings.Open(settingsFile)
	loop := core.NewLoop(nil)
	go loop.Run()

	var p *player.Player
	loop.Do(func() { p = player.New(loop, store, library.DefaultPath(settingsFile), audio.Options{}) })

	w := new(app.Window)
	w.Option(
		app.Title("Music Player"),
		app.Size(unit.Dp(1280), unit.Dp(800)),
		app.MinSize(unit.Dp(960), unit.Dp(600)),
		// Frameless: the main view's header doubles as the title bar, with the window controls in the corner.
		app.Decorated(false),
	)

	var (
		hwnd   uintptr
		native *win.Window
		tray   *win.Tray
		smtc   *win.SMTC
		u      *ui.UI
	)
	host := ui.Host{
		AddFolder: func() {
			go func() {
				if dir := win.PickFolder(hwnd, "Add a music folder"); dir != "" {
					loop.Post(func() { p.AddToLibrary([]string{dir}) })
				}
			}()
		},
		AddFiles: func() {
			go func() {
				if files := win.OpenFiles(hwnd, "Add music", formats.OpenFilters()); len(files) > 0 {
					loop.Post(func() { p.AddToLibrary(files) })
				}
			}()
		},
		ExportPlaylist: func(name string) {
			go func() {
				filters := []formats.Filter{{Name: "Playlists", Patterns: []string{"*.m3u8", "*.m3u"}}}
				if path := win.SaveFile(hwnd, "Export playlist", "m3u8", filters); path != "" {
					loop.Post(func() { p.ExportPlaylist(name, path) })
				}
			}()
		},
		ShowInExplorer: win.ShowInExplorer,
		SystemLight:    systemLight(),
		Event: func(e event.Event) {
			ve, ok := e.(app.Win32ViewEvent)
			if !ok || !ve.Valid() || native != nil {
				return
			}
			hwnd = ve.HWND
			native = win.AttachWindow(hwnd)
			native.SetIcon(win.NewIcon(16), win.NewIcon(32))
			native.EnableDrop(win.DropHandler{Over: u.OSDragOver, Drop: u.OSDrop})
			native.OnSettingChange = func() {
				refreshSystemTheme()
				u.Invalidate()
			}
			tray = win.NewTray(win.NewIcon(16), "Music Player", []win.MenuItem{
				{Label: "Show / Hide", Action: native.Toggle},
				{},
				{Label: "Play", Action: func() { loop.Post(p.Play) }},
				{Label: "Pause", Action: func() { loop.Post(p.Pause) }},
				{Label: "Next", Action: func() { loop.Post(p.Next) }},
				{},
				{Label: "Quit", Action: func() { w.Perform(system.ActionClose) }},
			}, native.Toggle)
			if tray != nil {
				native.OnMinimized = native.Hide // minimizing hides the window into the tray
			}
			smtc = win.NewSMTC(loop, p, hwnd)
			loop.Post(func() {
				tip := ""
				p.Observe(player.Observer{TrackChanged: func() {
					t := "Music Player"
					if cur := p.Current(); cur != nil {
						t = cur.DisplayName()
					}
					if t != tip {
						tip = t
						tray.SetTooltip(t)
					}
				}})
			})
		},
	}
	u = ui.New(w, loop, p, store, host)

	// Files and folders given on the command line ("Open with", file associations) are added and played.
	var paths []string
	for _, arg := range os.Args[1:] {
		if strings.HasPrefix(arg, "-") {
			continue
		}
		if abs, err := filepath.Abs(arg); err == nil {
			paths = append(paths, abs)
		}
	}
	if len(paths) > 0 {
		loop.Post(func() { p.OpenAndPlay(paths) })
	}

	go func() {
		err := u.Run()
		tray.Close()
		loop.Do(func() {
			smtc.Close()
			p.Close()
		})
		loop.Stop()
		if err != nil {
			log.Println(err)
			os.Exit(1)
		}
		os.Exit(0)
	}()
	app.Main()
}
