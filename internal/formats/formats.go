// Package formats is the single source of truth for what the player can open. The file dialog filters, the
// playlist/audio routing and the UI format badges all derive from it.
package formats

import (
	"path/filepath"
	"strings"
)

type Format struct {
	Badge     string // shown in the UI, e.g. "FLAC"
	Extension string // lower-case, no dot
	Lossless  bool
}

var all = []Format{
	{"FLAC", "flac", true},
	{"WAV", "wav", true},
	{"MP3", "mp3", false},
	{"OGG", "ogg", false},
	{"M4A", "m4a", false},
	{"AAC", "aac", false},
}

func ext(path string) string { return strings.ToLower(strings.TrimPrefix(filepath.Ext(path), ".")) }

// ForPath returns the format of path judged by its extension (no I/O), or nil.
func ForPath(path string) *Format {
	e := ext(path)
	for i := range all {
		if all[i].Extension == e {
			return &all[i]
		}
	}
	return nil
}

func IsAudio(path string) bool { return ForPath(path) != nil }

// IsPlaylist reports whether path is an .m3u / .m3u8 playlist.
func IsPlaylist(path string) bool {
	e := ext(path)
	return e == "m3u" || e == "m3u8"
}

// Badge is the UI label of path's format, empty if unsupported.
func Badge(path string) string {
	if f := ForPath(path); f != nil {
		return f.Badge
	}
	return ""
}

func IsLossless(path string) bool {
	f := ForPath(path)
	return f != nil && f.Lossless
}

// AudioPatterns returns {"*.flac", "*.wav", ...}.
func AudioPatterns() []string {
	p := make([]string, 0, len(all))
	for _, f := range all {
		p = append(p, "*."+f.Extension)
	}
	return p
}

// Filter is one entry of a native file dialog's type list.
type Filter struct {
	Name     string
	Patterns []string
}

// OpenFilters are the native open dialog's filters: Audio Files / Playlists / All Files.
func OpenFilters() []Filter {
	return []Filter{
		{"Audio Files", AudioPatterns()},
		{"Playlists", []string{"*.m3u", "*.m3u8"}},
		{"All Files", []string{"*.*"}},
	}
}
