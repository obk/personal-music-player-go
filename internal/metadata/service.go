package metadata

import (
	"bufio"
	"crypto/sha1"
	"encoding/hex"
	"image"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"

	"musicplayer/internal/core"
	"musicplayer/internal/formats"
	"musicplayer/internal/lyrics"
	"musicplayer/internal/settings"
)

// Per loop turn: deliver at most this many scan results; the rest follow in later turns.
const maxBatch = 200

type TagResult struct {
	Token     uint64
	Info      TrackInfo
	HasCover  bool
	HasLyrics bool
}

type NowPlaying struct {
	Request        uint64
	Path           string
	Large          *image.RGBA // up to 720 px, for the now-playing view; nil if the track has no art
	Medium         *image.RGBA // 256 px
	Small          *image.RGBA // 64 px
	Lyrics         []lyrics.Line
	LyricsFile     string // the sidecar .lrc used, "" if the lyrics were embedded (or absent)
	LyricsOffsetMs int    // the user's saved per-track correction
}

// LargeSide is the size the now-playing cover is kept at (the view never shows it bigger).
const LargeSide = 720

// Events are called on the loop. Any may be nil.
type Events struct {
	TagsReady       func(batch []TagResult)
	ScanIdle        func() // every scan so far has been delivered (or cancelled)
	NowPlayingReady func(np NowPlaying)
	PlaylistParsed  func(m3uPath string, paths []string, ok bool)
	CoverLoaded     func() // a RequestCover finished (the cache has new thumbnails)
}

type job struct {
	token uint64
	path  string
}

// Service does all tag, cover and lyrics reading off the loop.
//
//   - Scan: one extraction per file on workers bounded to the hardware concurrency (tags, audio properties,
//     ReplayGain, cover presence + 64/256 px thumbnails into the CoverCache, lyrics presence). Results come back on
//     the loop through TagsReady in small batches, each delivered in its own loop turn, so a 5,000-file add never
//     blocks a frame.
//   - Cancel: drops queued jobs by token; results of jobs already running are dropped by the receiver (it no
//     longer knows the token).
//   - LoadNowPlaying: the current track's large cover and parsed lyrics, on a separate small pool so it never
//     waits behind a large scan.
//   - ParsePlaylist: reads an .m3u/.m3u8 and checks which entries exist, off the loop.
type Service struct {
	loop   *core.Loop
	ev     Events
	covers *CoverCache
	store  *settings.Store

	mu               sync.Mutex // guards everything below
	queue            []job
	workers          int // scan workers currently running
	running          int // jobs taken off the queue but not yet in results
	results          []TagResult
	folderCover      map[string]string // directory -> folder cover file ("" = none)
	stopping         bool
	deliverScheduled atomic.Bool
	nextRequest      atomic.Uint64
	maxWorkers       int
	interactive      chan struct{} // semaphore of the interactive pool
	coverPool        chan struct{} // semaphore of on-demand cover loading
	coverReq         map[string]coverState
	wg               sync.WaitGroup
}

func NewService(loop *core.Loop, covers *CoverCache, store *settings.Store, ev Events) *Service {
	return &Service{
		loop: loop, ev: ev, covers: covers, store: store,
		folderCover: map[string]string{},
		// Scanning may use every core but one; the interactive pool (now playing, playlists) stays responsive.
		maxWorkers:  max(1, runtime.NumCPU()-1),
		interactive: make(chan struct{}, 2),
		coverPool:   make(chan struct{}, max(1, runtime.NumCPU()/2)),
		coverReq:    map[string]coverState{},
	}
}

type coverState uint8

const (
	coverLoading coverState = iota + 1
	coverAbsent
)

// RequestCover loads the 64 and 256 px thumbnails of path's cover (embedded, else folder art) into the cache in
// the background, for views that show many covers (the album grid): they may have been evicted since the scan.
// Repeated requests while one is running, or for files known to have no art, are ignored.
func (s *Service) RequestCover(path string) {
	s.mu.Lock()
	if s.stopping || s.coverReq[path] != 0 {
		s.mu.Unlock()
		return
	}
	s.coverReq[path] = coverLoading
	s.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.coverPool <- struct{}{}
		defer func() { <-s.coverPool }()
		ex := Extract(path, Cover)
		var medium *image.RGBA
		if len(ex.CoverData) > 0 {
			medium = DecodeCover(ex.CoverData, int(Medium))
		} else {
			medium = s.folderCoverImage(path, int(Medium))
		}
		s.mu.Lock()
		if medium == nil {
			s.coverReq[path] = coverAbsent
		} else {
			delete(s.coverReq, path) // evicted again later: may be requested again
		}
		stopping := s.stopping
		s.mu.Unlock()
		if medium != nil {
			s.covers.Insert(path, Medium, medium)
			s.covers.Insert(path, Small, Fit(medium, int(Small)))
		}
		if !stopping && s.ev.CoverLoaded != nil {
			s.loop.Post(s.ev.CoverLoaded)
		}
	}()
}

func (s *Service) Covers() *CoverCache { return s.covers }

// Close stops the workers: they finish their current file and exit; nothing is delivered afterwards.
func (s *Service) Close() {
	s.mu.Lock()
	s.stopping = true
	s.queue = nil
	s.mu.Unlock()
	s.wg.Wait()
}

type ScanJob struct {
	Token uint64
	Path  string
}

func (s *Service) Scan(jobs []ScanJob) {
	if len(jobs) == 0 {
		return
	}
	s.mu.Lock()
	for _, j := range jobs {
		s.queue = append(s.queue, job{j.Token, j.Path})
	}
	wanted := min(s.maxWorkers, len(s.queue))
	toStart := max(0, wanted-s.workers)
	s.workers += toStart
	s.mu.Unlock()
	for i := 0; i < toStart; i++ {
		s.wg.Add(1)
		go s.scanWorker()
	}
}

func (s *Service) Cancel(tokens map[uint64]bool) {
	if len(tokens) == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.queue[:0]
	for _, j := range s.queue {
		if !tokens[j.token] {
			q = append(q, j)
		}
	}
	s.queue = q
	r := s.results[:0]
	for _, res := range s.results {
		if !tokens[res.Token] {
			r = append(r, res)
		}
	}
	s.results = r
}

func (s *Service) CancelAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.queue = nil
	s.results = nil
}

// PendingScans counts queued + running + not yet delivered scans.
func (s *Service) PendingScans() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queue) + s.running + len(s.results)
}

// One worker per core, each pulling jobs until the queue is empty (cheap to submit 5,000 files, and
// cancellation is just removing queue entries).
func (s *Service) scanWorker() {
	defer s.wg.Done()
	for {
		s.mu.Lock()
		if s.stopping || len(s.queue) == 0 {
			s.workers--
			s.mu.Unlock()
			return
		}
		j := s.queue[0]
		s.queue = s.queue[1:]
		s.running++
		s.mu.Unlock()

		r := s.scanOne(j.path)
		r.Token = j.token

		s.mu.Lock()
		s.running--
		if s.stopping {
			s.mu.Unlock()
			continue
		}
		s.results = append(s.results, r)
		s.mu.Unlock()
		if !s.deliverScheduled.Swap(true) {
			s.loop.Post(s.deliver)
		}
	}
}

func (s *Service) scanOne(path string) TagResult {
	ex := Extract(path, Tags|Cover)
	r := TagResult{Info: ex.Info, HasLyrics: ex.HasLyrics}
	// Decode once at 256 px and derive 64 px from it; the full-size image is never held.
	var medium *image.RGBA
	if len(ex.CoverData) > 0 {
		medium = DecodeCover(ex.CoverData, int(Medium))
	} else {
		medium = s.folderCoverImage(path, int(Medium))
	}
	if medium != nil {
		r.HasCover = true
		s.covers.Insert(path, Medium, medium)
		s.covers.Insert(path, Small, Fit(medium, int(Small)))
	}
	return r
}

// folderCoverImage: folder art is shared by a whole album, so the directory is listed once and the decoded
// image is shared by every track of the album.
func (s *Service) folderCoverImage(audioPath string, maxSide int) *image.RGBA {
	dir := filepath.Dir(audioPath)
	s.mu.Lock()
	file, known := s.folderCover[dir]
	s.mu.Unlock()
	if !known {
		file = FolderCoverPath(audioPath)
		s.mu.Lock()
		s.folderCover[dir] = file
		s.mu.Unlock()
	}
	if file == "" {
		return nil
	}
	size := Medium
	if maxSide > 0 && maxSide <= int(Small) {
		size = Small
	}
	// Other tracks of the album may have decoded it already.
	if maxSide > 0 {
		if cached := s.covers.Find(file, size); cached != nil {
			return cached
		}
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return nil
	}
	img := DecodeCover(data, maxSide)
	if maxSide > 0 {
		s.covers.Insert(file, size, img)
	}
	return img
}

// deliver runs on the loop.
func (s *Service) deliver() {
	s.deliverScheduled.Store(false)
	s.mu.Lock()
	n := min(maxBatch, len(s.results))
	batch := append([]TagResult(nil), s.results[:n]...)
	s.results = s.results[n:]
	more := len(s.results) > 0
	idle := !more && len(s.queue) == 0 && s.running == 0
	s.mu.Unlock()
	if len(batch) > 0 && s.ev.TagsReady != nil {
		s.ev.TagsReady(batch)
	}
	// Anything left goes out in a later loop turn, after pending UI frames.
	if more && !s.deliverScheduled.Swap(true) {
		s.loop.Post(s.deliver)
	}
	if idle && s.ev.ScanIdle != nil {
		s.ev.ScanIdle()
	}
}

// LyricsOffsetKey is the settings key of a track's lyric offset: a hash of its path.
func LyricsOffsetKey(audioPath string) string {
	h := sha1.Sum([]byte(audioPath))
	return hex.EncodeToString(h[:])
}

func (s *Service) runInteractive(f func()) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.interactive <- struct{}{}
		defer func() { <-s.interactive }()
		f()
	}()
}

func (s *Service) isStopping() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopping
}

// LoadNowPlaying reads the track's large cover and its lyrics (embedded, else a sidecar <basename>.lrc) and
// delivers them through NowPlayingReady. It returns the request number the result will carry.
func (s *Service) LoadNowPlaying(path string) uint64 {
	request := s.nextRequest.Add(1)
	s.runInteractive(func() {
		ex := Extract(path, Cover|Lyrics)
		np := NowPlaying{Request: request, Path: path}
		var full *image.RGBA
		if len(ex.CoverData) > 0 {
			full = DecodeCover(ex.CoverData, LargeSide)
		} else {
			full = s.folderCoverImage(path, LargeSide)
		}
		if full != nil {
			np.Large = full
			np.Medium = Fit(full, int(Medium))
			np.Small = Fit(np.Medium, int(Small))
			s.covers.Insert(path, Medium, np.Medium)
			s.covers.Insert(path, Small, np.Small)
		}
		// Embedded lyrics first; plain (untimed) embedded text can't be shown, so then <basename>.lrc beside it.
		np.Lyrics = lyrics.Parse(ex.Lyrics)
		if len(np.Lyrics) == 0 {
			lrc := strings.TrimSuffix(path, filepath.Ext(path)) + ".lrc"
			if fi, err := os.Stat(lrc); err == nil && fi.Size() < 4<<20 {
				if data, err := os.ReadFile(lrc); err == nil {
					np.Lyrics = lyrics.Parse(lyrics.Decode(data))
					if len(np.Lyrics) > 0 {
						np.LyricsFile = lrc
					}
				}
			}
		}
		np.LyricsOffsetMs = s.store.Get().LyricsOffsets[LyricsOffsetKey(path)]
		if s.isStopping() {
			return
		}
		s.loop.Post(func() {
			if s.ev.NowPlayingReady != nil {
				s.ev.NowPlayingReady(np)
			}
		})
	})
	return request
}

// ParsePlaylist reads an .m3u/.m3u8 off the loop and reports the entries that are supported and exist.
func (s *Service) ParsePlaylist(m3uPath string) {
	s.runInteractive(func() {
		paths, ok := parseM3u(m3uPath)
		if s.isStopping() {
			return
		}
		s.loop.Post(func() {
			if s.ev.PlaylistParsed != nil {
				s.ev.PlaylistParsed(m3uPath, paths, ok)
			}
		})
	})
}

func parseM3u(m3uPath string) ([]string, bool) {
	f, err := os.Open(m3uPath)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	base, _ := filepath.Abs(filepath.Dir(m3uPath))
	var paths []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	first := true
	for sc.Scan() {
		line := sc.Text()
		if first {
			line = strings.TrimPrefix(line, "\uFEFF")
			first = false
		}
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "file://") {
			if u, err := url.Parse(line); err == nil {
				p := u.Path
				if len(p) > 2 && p[0] == '/' && p[2] == ':' { // file:///C:/...
					p = p[1:]
				}
				line = filepath.FromSlash(p)
			}
		}
		abs := line
		if !filepath.IsAbs(line) {
			abs = filepath.Join(base, line)
		}
		abs = filepath.Clean(abs)
		if formats.IsAudio(abs) {
			if _, err := os.Stat(abs); err == nil {
				paths = append(paths, abs)
			}
		}
	}
	return paths, true
}
