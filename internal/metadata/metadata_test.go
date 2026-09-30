package metadata

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"musicplayer/internal/core"
	"musicplayer/internal/settings"
)

// fixtures builds tagged files with the ffmpeg tool: embedded 600 px cover, lyrics, ReplayGain.
func fixtures(t *testing.T) string {
	t.Helper()
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not found: tagged fixtures skipped")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		if out, err := exec.Command(ff, append([]string{"-loglevel", "error", "-y"}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("ffmpeg %v: %v\n%s", args, err, out)
		}
	}
	cover := filepath.Join(dir, "cover.jpg")
	run("-f", "lavfi", "-i", "testsrc2=size=600x600:rate=1", "-frames:v", "1", cover)
	for _, v := range []string{"a", "b"} {
		run("-f", "lavfi", "-i", "sine=frequency=440:duration=1:sample_rate=44100", "-i", cover,
			"-map", "0:a", "-map", "1:v", "-c:a", "flac", "-c:v", "mjpeg", "-disposition:v", "attached_pic",
			"-metadata", "title=Title "+v, "-metadata", "artist=Artist "+v, "-metadata", "album=Album",
			"-metadata", "LYRICS=[00:00.50]first line "+v, "-metadata", "REPLAYGAIN_TRACK_GAIN=-6.50 dB",
			"-metadata", "date=2019-05-01", "-metadata", "track=3/12",
			filepath.Join(dir, "tagged_"+v+".flac"))
	}
	run("-f", "lavfi", "-i", "sine=frequency=440:duration=1", "-i", cover,
		"-map", "0:a", "-map", "1:v", "-c:a", "libmp3lame", "-c:v", "mjpeg", "-disposition:v", "attached_pic",
		"-id3v2_version", "3", "-metadata", "title=Title mp3", "-metadata", "artist=Artist mp3",
		"-metadata", "lyrics=[00:00.50]first line mp3", filepath.Join(dir, "tagged.mp3"))
	run("-f", "lavfi", "-i", "anoisesrc=d=2:c=white:r=48000:a=0.4", "-ac", "2", filepath.Join(dir, "noise.flac"))
	return dir
}

func TestExtract(t *testing.T) {
	dir := fixtures(t)
	ex := Extract(filepath.Join(dir, "tagged_a.flac"), Tags|Cover|Lyrics)
	i := ex.Info
	if i.Title != "Title a" || i.Artist != "Artist a" || i.Album != "Album" || i.Format != "FLAC" || !i.Lossless {
		t.Errorf("flac tags: %+v", i)
	}
	if i.Year != 2019 || i.TrackNumber != 3 || i.SampleRate != 44100 || i.DurationMs < 950 || i.DurationMs > 1050 {
		t.Errorf("flac properties: %+v", i)
	}
	if math.Abs(i.ReplayGain.TrackGain+6.5) > 1e-9 || !math.IsNaN(i.ReplayGain.AlbumGain) {
		t.Errorf("replaygain %+v", i.ReplayGain)
	}
	if !ex.HasCover || len(ex.CoverData) == 0 || !ex.HasLyrics || ex.Lyrics != "[00:00.50]first line a" {
		t.Errorf("flac cover %v (%d bytes), lyrics %q", ex.HasCover, len(ex.CoverData), ex.Lyrics)
	}
	img := DecodeCover(ex.CoverData, 256)
	if img == nil || img.Bounds().Dx() != 256 || img.Bounds().Dy() != 256 {
		t.Errorf("cover decode: %v", img)
	}

	mp3 := Extract(filepath.Join(dir, "tagged.mp3"), Tags|CoverPresence)
	if mp3.Info.Title != "Title mp3" || mp3.Info.Artist != "Artist mp3" || !mp3.HasCover || !mp3.HasLyrics ||
		mp3.Info.Bitrate <= 0 || mp3.CoverData != nil {
		t.Errorf("mp3: %+v cover %v lyrics %v", mp3.Info, mp3.HasCover, mp3.HasLyrics)
	}

	missing := Extract(filepath.Join(dir, "does not exist.flac"), Tags)
	if missing.Info.Title != "does not exist" || missing.Info.Format != "FLAC" {
		t.Errorf("missing file placeholder: %+v", missing.Info)
	}
}

// id3Sylt builds an ID3v2.3 tag holding one SYLT frame (UTF-16 with BOM, millisecond timestamps).
func id3Sylt(lines map[uint32]string, order []uint32) []byte {
	var body bytes.Buffer
	body.Write([]byte{1, 'e', 'n', 'g', 2, 1}) // UTF-16, language, ms timestamps, content type lyrics
	body.Write([]byte{0xFF, 0xFE, 0, 0})       // empty descriptor
	for _, t := range order {
		body.Write([]byte{0xFF, 0xFE})
		for _, r := range lines[t] {
			binary.Write(&body, binary.LittleEndian, uint16(r))
		}
		body.Write([]byte{0, 0})
		binary.Write(&body, binary.BigEndian, t)
	}
	var frame bytes.Buffer
	frame.WriteString("SYLT")
	binary.Write(&frame, binary.BigEndian, uint32(body.Len()))
	frame.Write([]byte{0, 0})
	frame.Write(body.Bytes())
	size := frame.Len()
	hdr := []byte{'I', 'D', '3', 3, 0, 0, byte(size >> 21 & 0x7f), byte(size >> 14 & 0x7f), byte(size >> 7 & 0x7f), byte(size & 0x7f)}
	return append(hdr, frame.Bytes()...)
}

func TestSylt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sylt.mp3")
	tag := id3Sylt(map[uint32]string{1500: "hello", 62040: "\nworld"}, []uint32{1500, 62040})
	if err := os.WriteFile(path, tag, 0o644); err != nil {
		t.Fatal(err)
	}
	if got, want := SyncedLyricsFromID3(path), "[00:01.50]hello\n[01:02.04]world\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func newService(t *testing.T) (*Service, *core.Loop, chan NowPlaying) {
	loop := core.NewLoop(nil)
	go loop.Run()
	got := make(chan NowPlaying, 4)
	svc := NewService(loop, NewCoverCache(64<<20), settings.Open(""), Events{
		NowPlayingReady: func(np NowPlaying) { got <- np },
	})
	t.Cleanup(func() { svc.Close(); loop.Stop() })
	return svc, loop, got
}

func TestNowPlayingAndSidecar(t *testing.T) {
	fx := fixtures(t)
	svc, _, got := newService(t)
	load := func(path string) NowPlaying {
		req := svc.LoadNowPlaying(path)
		for {
			select {
			case np := <-got:
				if np.Request == req {
					return np
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("no result for %s", path)
			}
		}
	}
	copyFile := func(src, dst string) {
		b, _ := os.ReadFile(src)
		os.WriteFile(dst, b, 0o644)
	}
	dir := t.TempDir()

	np := load(filepath.Join(fx, "tagged_b.flac"))
	if np.Large == nil || np.Medium.Bounds().Dx() != 256 || np.Small.Bounds().Dx() != 64 {
		t.Error("now-playing covers")
	}
	if len(np.Lyrics) != 1 || np.Lyrics[0].Text != "first line b" || np.LyricsFile != "" {
		t.Errorf("embedded lyrics: %+v", np.Lyrics)
	}
	if svc.Covers().Find(filepath.Join(fx, "tagged_b.flac"), Medium) == nil {
		t.Error("thumbnail not cached")
	}

	// No embedded lyrics: the Windows-1252 sidecar is used and decoded.
	plain := filepath.Join(dir, "sidecar song.flac")
	copyFile(filepath.Join(fx, "noise.flac"), plain)
	os.WriteFile(filepath.Join(dir, "sidecar song.lrc"),
		[]byte("[ti:x]\r\n[00:01.00]Caf\xe9 d\xe9j\xe0 vu\r\n[00:02.50]\xe0 bient\xf4t\r\n"), 0o644)
	np = load(plain)
	if len(np.Lyrics) != 2 || np.Lyrics[0].Text != "Café déjà vu" || np.Lyrics[1].Text != "à bientôt" ||
		filepath.Base(np.LyricsFile) != "sidecar song.lrc" {
		t.Errorf("sidecar: %+v from %q", np.Lyrics, np.LyricsFile)
	}

	// Embedded lyrics take precedence over a sidecar.
	emb := filepath.Join(dir, "embedded.flac")
	copyFile(filepath.Join(fx, "tagged_a.flac"), emb)
	os.WriteFile(filepath.Join(dir, "embedded.lrc"), []byte("[00:01.00]from the sidecar\n"), 0o644)
	np = load(emb)
	if len(np.Lyrics) == 0 || np.Lyrics[0].Text != "first line a" || np.LyricsFile != "" {
		t.Errorf("embedded should win: %+v", np.Lyrics)
	}
}

func TestScanAndCancel(t *testing.T) {
	fx := fixtures(t)
	loop := core.NewLoop(nil)
	go loop.Run()
	results := make(chan []TagResult, 1000)
	svc := NewService(loop, NewCoverCache(64<<20), settings.Open(""), Events{
		TagsReady: func(b []TagResult) { results <- b },
	})
	defer func() { svc.Close(); loop.Stop() }()

	var jobs []ScanJob
	for i := 0; i < 300; i++ {
		v := []string{"a", "b"}[i%2]
		jobs = append(jobs, ScanJob{uint64(i + 1), filepath.Join(fx, "tagged_"+v+".flac")})
	}
	svc.Scan(jobs)
	seen := map[uint64]TagResult{}
	deadline := time.After(20 * time.Second)
	for len(seen) < len(jobs) {
		select {
		case b := <-results:
			for _, r := range b {
				seen[r.Token] = r
			}
		case <-deadline:
			t.Fatalf("only %d of %d results", len(seen), len(jobs))
		}
	}
	for tok, r := range seen {
		want := []string{"Title a", "Title b"}[(tok-1)%2]
		if r.Info.Title != want || !r.HasCover || !r.HasLyrics {
			t.Fatalf("token %d: %+v", tok, r)
		}
	}

	// Cancelling queued work: nothing for those tokens arrives any more.
	svc.Scan(jobs)
	svc.CancelAll()
	time.Sleep(500 * time.Millisecond)
	if n := svc.PendingScans(); n != 0 {
		t.Errorf("%d scans pending after CancelAll", n)
	}
}

func TestParseM3u(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.flac")
	os.WriteFile(a, nil, 0o644)
	os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	b := filepath.Join(dir, "sub", "b.mp3")
	os.WriteFile(b, nil, 0o644)
	m3u := filepath.Join(dir, "list.m3u8")
	os.WriteFile(m3u, []byte("\xEF\xBB\xBF#EXTM3U\n#EXTINF:1,x\na.flac\r\nsub/b.mp3\nmissing.flac\nnotes.txt\n"), 0o644)
	paths, ok := parseM3u(m3u)
	if !ok || len(paths) != 2 || paths[0] != a || paths[1] != b {
		t.Errorf("parsed %v %v", paths, ok)
	}
	if _, ok := parseM3u(filepath.Join(dir, "nope.m3u")); ok {
		t.Error("missing playlist parsed")
	}
}

func TestWriteTags(t *testing.T) {
	fx := fixtures(t)
	dir := t.TempDir()
	for _, name := range []string{"tagged_a.flac", "tagged.mp3"} {
		path := filepath.Join(dir, name)
		b, _ := os.ReadFile(filepath.Join(fx, name))
		os.WriteFile(path, b, 0o644)
		before := Extract(path, Tags|CoverPresence)
		want := TagFields{Title: "New Title", Artist: "New Artist", Album: "New Album", AlbumArtist: "Various",
			Genre: "Jazz", Year: 1999, TrackNumber: 7, DiscNumber: 2}
		if err := WriteTags(path, want); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		after := Extract(path, Tags|CoverPresence|Lyrics)
		if got := FieldsOf(after.Info); got != want {
			t.Errorf("%s: read back %+v", name, got)
		}
		if !after.HasCover {
			t.Errorf("%s: cover lost", name)
		}
		if !after.HasLyrics {
			t.Errorf("%s: other tags (lyrics) lost", name)
		}
		if d := after.Info.DurationMs - before.Info.DurationMs; d > 60 || d < -60 {
			t.Errorf("%s: duration %d -> %d ms", name, before.Info.DurationMs, after.Info.DurationMs)
		}
		// Clearing a field removes it.
		want.Genre, want.DiscNumber = "", 0
		if err := WriteTags(path, want); err != nil {
			t.Fatal(err)
		}
		if got := FieldsOf(Extract(path, Tags).Info); got != want {
			t.Errorf("%s: after clearing: %+v", name, got)
		}
		if matches, _ := filepath.Glob(filepath.Join(dir, "*.mptags*")); len(matches) > 0 {
			t.Errorf("temporary files left: %v", matches)
		}
	}
}
