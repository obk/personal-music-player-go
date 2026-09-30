// Package settings persists the player's preferences as JSON in the user's config directory
// (%AppData%\MusicPlayer\settings.json on Windows). Safe for concurrent use.
package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

type EqPreset struct {
	PreampDb float64   `json:"preampDb"`
	GainsDb  []float64 `json:"gainsDb"`
}

// Values is the persisted document.
type Values struct {
	OutputDevice string              `json:"outputDevice,omitempty"`
	Exclusive    bool                `json:"exclusive,omitempty"`
	EqEnabled    bool                `json:"eqEnabled,omitempty"`
	EqPreampDb   float64             `json:"eqPreampDb,omitempty"`
	EqGainsDb    []float64           `json:"eqGainsDb,omitempty"`
	EqPresets    map[string]EqPreset `json:"eqPresets,omitempty"`
	Theme        string              `json:"theme,omitempty"` // "" (follow the system), "dark" or "light"
	QueueOpen    bool                `json:"queueOpen,omitempty"`
	Volume       *float64            `json:"volume,omitempty"`
	// Per-track lyric timing correction in ms, keyed by LyricsOffsetKey(path).
	LyricsOffsets map[string]int `json:"lyricsOffsets,omitempty"`
}

type Store struct {
	mu   sync.Mutex
	path string // empty: in memory only (tests)
	v    Values
}

// Open loads the store at path; a missing or unreadable file starts empty. An empty path keeps everything in memory.
func Open(path string) *Store {
	s := &Store{path: path}
	if path != "" {
		if b, err := os.ReadFile(path); err == nil {
			_ = json.Unmarshal(b, &s.v)
		}
	}
	return s
}

// DefaultPath is %AppData%\MusicPlayer\settings.json (or the platform's equivalent).
func DefaultPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "MusicPlayer", "settings.json")
}

// Get returns a deep copy of the current values.
func (s *Store) Get() Values {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clone(s.v)
}

// Update changes the values with fn and writes the file.
func (s *Store) Update(fn func(v *Values)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fn(&s.v)
	s.save()
}

func (s *Store) save() {
	if s.path == "" {
		return
	}
	b, err := json.MarshalIndent(s.v, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return
	}
	// Write-then-rename, so a crash mid-write never leaves a truncated file.
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err == nil {
		_ = os.Rename(tmp, s.path)
	}
}

func clone(v Values) Values {
	c := v
	c.EqGainsDb = append([]float64(nil), v.EqGainsDb...)
	if v.EqPresets != nil {
		c.EqPresets = make(map[string]EqPreset, len(v.EqPresets))
		for k, p := range v.EqPresets {
			c.EqPresets[k] = EqPreset{p.PreampDb, append([]float64(nil), p.GainsDb...)}
		}
	}
	if v.LyricsOffsets != nil {
		c.LyricsOffsets = make(map[string]int, len(v.LyricsOffsets))
		for k, o := range v.LyricsOffsets {
			c.LyricsOffsets[k] = o
		}
	}
	return c
}
