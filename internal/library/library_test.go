package library

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"musicplayer/internal/core"
	"musicplayer/internal/metadata"
)

// fakeScanner "reads" tags from the file name: "Artist - Album - 03 - Title.flac".
type fakeScanner struct{ lib *Library }

func (f *fakeScanner) Scan(jobs []metadata.ScanJob) {
	var batch []metadata.TagResult
	for _, j := range jobs {
		info := metadata.Placeholder(j.Path)
		parts := strings.Split(strings.TrimSuffix(filepath.Base(j.Path), filepath.Ext(j.Path)), " - ")
		if len(parts) == 4 {
			info.Artist, info.Album, info.Title = parts[0], parts[1], parts[3]
			info.TrackNumber = int(parts[2][1] - '0')
		}
		info.DurationMs = 60000
		batch = append(batch, metadata.TagResult{Token: j.Token, Info: info})
	}
	go f.lib.loop.Post(func() { f.lib.OnTagsReady(batch) })
}
func (f *fakeScanner) Cancel(map[uint64]bool) {}

func touch(t *testing.T, path string) string {
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func newLib(t *testing.T, file string) (*Library, *core.Loop) {
	loop := core.NewLoop(nil)
	go loop.Run()
	t.Cleanup(loop.Stop)
	var l *Library
	loop.Do(func() {
		sc := &fakeScanner{}
		l = New(loop, sc, file)
		sc.lib = l
	})
	return l, loop
}

func waitIdle(t *testing.T, loop *core.Loop, l *Library) {
	t.Helper()
	for i := 0; i < 200; i++ {
		busy := true
		loop.Do(func() { busy = l.Scanning() })
		if !busy {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("library never finished scanning")
}

func TestLibrary(t *testing.T) {
	dir := t.TempDir()
	music := filepath.Join(dir, "Music")
	touch(t, filepath.Join(music, "A", "Band - Record - 02 - Second.flac"))
	touch(t, filepath.Join(music, "A", "Band - Record - 01 - First.flac"))
	touch(t, filepath.Join(music, "B", "Solo - Tape - 01 - Only.mp3"))
	touch(t, filepath.Join(music, "B", "notes.txt"))
	loose := touch(t, filepath.Join(dir, "Other - Single - 01 - Loose.wav"))
	file := filepath.Join(dir, "library.json")

	l, loop := newLib(t, file)
	loop.Do(func() {
		l.Load()
		l.AddPaths([]string{music, loose})
	})
	waitIdle(t, loop, l)
	loop.Do(func() {
		if l.Count() != 4 {
			t.Fatalf("%d tracks", l.Count())
		}
		var titles []string
		for _, s := range l.Songs() {
			titles = append(titles, s.Info.Title)
		}
		if got := strings.Join(titles, ","); got != "First,Second,Loose,Only" {
			t.Errorf("songs order: %s", got)
		}
		if len(l.Albums()) != 3 || len(l.Artists()) != 3 || len(l.FolderList()) != 3 {
			t.Errorf("%d albums %d artists %d folders", len(l.Albums()), len(l.Artists()), len(l.FolderList()))
		}
		rec := l.Album(albumKey(l.Songs()[0]))
		if rec == nil || rec.Title != "Record" || len(rec.Tracks) != 2 || rec.Tracks[0].Info.Title != "First" {
			t.Errorf("album %+v", rec)
		}
		if got := len(l.Search("band sec")); got != 1 {
			t.Errorf("search: %d", got)
		}
		name := l.NewPlaylist("Mix", IDs(l.Songs()[:2]))
		l.AddToPlaylist(name, []string{l.Songs()[3].ID}, 0)
		l.ToggleLike(l.Songs()[1].ID)
		// Removing a track in a watched folder keeps it out of rescans.
		l.Remove([]string{l.Songs()[2].ID})
		l.Save()
	})

	// A new file, a deleted file, and the saved state.
	os.Remove(filepath.Join(music, "A", "Band - Record - 02 - Second.flac"))
	touch(t, filepath.Join(music, "B", "Solo - Tape - 02 - New.mp3"))
	l2, loop2 := newLib(t, file)
	loop2.Do(l2.Load)
	waitIdle(t, loop2, l2)
	loop2.Do(func() {
		var titles []string
		for _, s := range l2.Songs() {
			titles = append(titles, s.Info.Title)
		}
		if got := strings.Join(titles, ","); got != "First,Only,New" {
			t.Errorf("after rescan: %s", got)
		}
		pl := l2.Playlist("Mix")
		if pl == nil || len(pl.IDs) != 2 { // "Second" is gone from disk
			t.Fatalf("playlist %+v", pl)
		}
		if len(l2.Liked()) != 0 {
			t.Error("the liked track was deleted from disk and should be gone")
		}
	})
}

func TestMoveItems(t *testing.T) {
	got := strings.Join(moveItems(strings.Split("abcdef", ""), []int{1, 4}, 3), "")
	if got != "acdbef" {
		t.Errorf("moveItems: %s", got)
	}
	if FormatDuration(3723000) != "1:02:03" || FormatDuration(61000) != "1:01" {
		t.Error("FormatDuration")
	}
}
