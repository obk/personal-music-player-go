// Package library is the collection of tracks the player knows about: watched folders, individually added files,
// their cached tags, user playlists and liked songs. It is persisted as JSON, rescanned in the background at
// startup, and offers the sorted views the UI shows (songs, albums, artists, folders, search).
//
// All methods must be called on the loop.
package library

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"musicplayer/internal/core"
	"musicplayer/internal/formats"
	"musicplayer/internal/metadata"
)

type Track struct {
	ID        string // the key: the cleaned absolute path, lower-case
	Path      string
	Info      metadata.TrackInfo
	ModTime   int64
	Size      int64
	HasCover  bool
	HasLyrics bool
	Pending   bool // tags not read yet
	token     uint64
}

// ArtistName is the artist a track is filed under (album artist first).
func (t *Track) ArtistName() string {
	if t.Info.AlbumArtist != "" {
		return t.Info.AlbumArtist
	}
	return t.Info.Artist
}

// Duration is "m:ss" (or "h:mm:ss"), empty while the tags are pending.
func (t *Track) Duration() string {
	if t.Pending {
		return ""
	}
	return FormatDuration(t.Info.DurationMs)
}

// FormatDuration formats milliseconds as m:ss, or h:mm:ss from one hour up.
func FormatDuration(ms int64) string {
	s := max(ms, 0) / 1000
	if s >= 3600 {
		return strconv.FormatInt(s/3600, 10) + ":" + pad2(s/60%60) + ":" + pad2(s%60)
	}
	return strconv.FormatInt(s/60, 10) + ":" + pad2(s%60)
}

func pad2(v int64) string {
	if v < 10 {
		return "0" + strconv.FormatInt(v, 10)
	}
	return strconv.FormatInt(v, 10)
}

// DisplayName is "Artist – Title" (or just the title).
func (t *Track) DisplayName() string {
	if t.Info.Artist == "" {
		return t.Info.Title
	}
	return t.Info.Artist + " – " + t.Info.Title
}

type Album struct {
	Key    string
	Title  string // "" = unknown
	Artist string // "" = unknown
	Year   int
	Tracks []*Track // by disc and track number
}

// CoverTrack is the track whose art represents the album.
func (a *Album) CoverTrack() *Track {
	for _, t := range a.Tracks {
		if t.HasCover {
			return t
		}
	}
	return a.Tracks[0]
}

type Artist struct {
	Key    string
	Name   string // "" = unknown
	Albums int
	Tracks []*Track
}

type Folder struct {
	Path   string
	Tracks []*Track
}

type Playlist struct {
	Name string
	IDs  []string
}

// ID returns the library key of path.
func ID(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	return strings.ToLower(filepath.Clean(path))
}

// Scanner is what the library needs from the metadata service.
type Scanner interface {
	Scan(jobs []metadata.ScanJob)
	Cancel(tokens map[uint64]bool)
}

type Library struct {
	loop     *core.Loop
	scanner  Scanner
	file     string // library.json ("" = not persisted)
	tracks   map[string]*Track
	byToken  map[uint64]*Track
	next     uint64
	folders  []string        // watched roots
	excluded map[string]bool // removed by the user although inside a watched folder
	lists    []*Playlist
	liked    []string
	likedSet map[string]bool
	rev      uint64
	view     views
	save     *core.Timer
	walking  int

	// OnChange is called after any change (tracks, tags, playlists, likes).
	OnChange func()
	// OnRemoved is called with the IDs of tracks that left the library.
	OnRemoved func(ids []string)
}

type views struct {
	rev          uint64
	songs        []*Track
	albums       []*Album
	albumByKey   map[string]*Album
	artists      []*Artist
	artistByKey  map[string]*Artist
	folders      []*Folder
	folderByPath map[string]*Folder
}

// New returns an empty library persisted at file (loaded by Load).
func New(loop *core.Loop, scanner Scanner, file string) *Library {
	l := &Library{loop: loop, scanner: scanner, file: file, tracks: map[string]*Track{},
		byToken: map[uint64]*Track{}, excluded: map[string]bool{}, likedSet: map[string]bool{}}
	l.save = loop.NewTimer(2*time.Second, false, l.Save)
	return l
}

// DefaultPath is library.json next to the settings.
func DefaultPath(settingsFile string) string {
	if settingsFile == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(settingsFile), "library.json")
}

func (l *Library) changed() {
	l.rev++
	if l.file != "" {
		l.save.Start()
	}
	if l.OnChange != nil {
		l.OnChange()
	}
}

// Rev increases with every change; views derived from the library can be cached per revision.
func (l *Library) Rev() uint64 { return l.rev }

// Scanning reports whether folders are being walked or tags read.
func (l *Library) Scanning() bool {
	if l.walking > 0 {
		return true
	}
	for _, t := range l.tracks {
		if t.Pending {
			return true
		}
	}
	return false
}

func (l *Library) Track(id string) *Track { return l.tracks[id] }
func (l *Library) Count() int             { return len(l.tracks) }
func (l *Library) Folders() []string      { return l.folders }

// ---- persistence ----

type fileTrack struct {
	Path        string `json:"path"`
	Title       string `json:"title,omitempty"`
	Artist      string `json:"artist,omitempty"`
	Album       string `json:"album,omitempty"`
	AlbumArtist string `json:"albumArtist,omitempty"`
	Genre       string `json:"genre,omitempty"`
	Year        int    `json:"year,omitempty"`
	Track       int    `json:"track,omitempty"`
	Disc        int    `json:"disc,omitempty"`
	DurationMs  int64  `json:"durationMs,omitempty"`
	SampleRate  int    `json:"sampleRate,omitempty"`
	Bitrate     int    `json:"bitrate,omitempty"`
	ModTime     int64  `json:"modTime"`
	Size        int64  `json:"size"`
	HasCover    bool   `json:"hasCover,omitempty"`
	HasLyrics   bool   `json:"hasLyrics,omitempty"`
}

type filePlaylist struct {
	Name  string   `json:"name"`
	Paths []string `json:"paths"`
}

type fileFormat struct {
	Version   int            `json:"version"`
	Folders   []string       `json:"folders"`
	Excluded  []string       `json:"excluded,omitempty"`
	Tracks    []fileTrack    `json:"tracks"`
	Playlists []filePlaylist `json:"playlists,omitempty"`
	Liked     []string       `json:"liked,omitempty"`
}

// Load reads the saved library and starts a background rescan of the watched folders (new, changed and removed
// files) and of the individually added files.
func (l *Library) Load() {
	if l.file != "" {
		if b, err := os.ReadFile(l.file); err == nil {
			var f fileFormat
			if json.Unmarshal(b, &f) == nil {
				l.restore(f)
			}
		}
	}
	l.rev++
	l.Rescan()
}

func (l *Library) restore(f fileFormat) {
	l.folders = f.Folders
	for _, p := range f.Excluded {
		l.excluded[ID(p)] = true
	}
	for _, ft := range f.Tracks {
		info := metadata.Placeholder(ft.Path)
		info.Title = firstNonEmpty(ft.Title, info.Title)
		info.Artist, info.Album, info.AlbumArtist, info.Genre = ft.Artist, ft.Album, ft.AlbumArtist, ft.Genre
		info.Year, info.TrackNumber, info.DiscNumber = ft.Year, ft.Track, ft.Disc
		info.DurationMs, info.SampleRate, info.Bitrate = ft.DurationMs, ft.SampleRate, ft.Bitrate
		t := &Track{ID: ID(ft.Path), Path: ft.Path, Info: info, ModTime: ft.ModTime, Size: ft.Size,
			HasCover: ft.HasCover, HasLyrics: ft.HasLyrics}
		l.tracks[t.ID] = t
	}
	for _, p := range f.Playlists {
		pl := &Playlist{Name: p.Name}
		for _, path := range p.Paths {
			if id := ID(path); l.tracks[id] != nil {
				pl.IDs = append(pl.IDs, id)
			}
		}
		l.lists = append(l.lists, pl)
	}
	for _, path := range f.Liked {
		if id := ID(path); l.tracks[id] != nil && !l.likedSet[id] {
			l.liked = append(l.liked, id)
			l.likedSet[id] = true
		}
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// Save writes the library file now.
func (l *Library) Save() {
	l.save.Stop()
	if l.file == "" {
		return
	}
	f := fileFormat{Version: 1, Folders: l.folders}
	for id := range l.excluded {
		f.Excluded = append(f.Excluded, id)
	}
	slices.Sort(f.Excluded)
	for _, t := range l.sortedByPath() {
		if t.Pending && t.ModTime == 0 {
			continue // never read: scanned again next time
		}
		i := t.Info
		f.Tracks = append(f.Tracks, fileTrack{Path: t.Path, Title: i.Title, Artist: i.Artist, Album: i.Album,
			AlbumArtist: i.AlbumArtist, Genre: i.Genre, Year: i.Year, Track: i.TrackNumber, Disc: i.DiscNumber,
			DurationMs: i.DurationMs, SampleRate: i.SampleRate, Bitrate: i.Bitrate, ModTime: t.ModTime, Size: t.Size,
			HasCover: t.HasCover, HasLyrics: t.HasLyrics})
	}
	for _, pl := range l.lists {
		fp := filePlaylist{Name: pl.Name, Paths: []string{}}
		for _, id := range pl.IDs {
			fp.Paths = append(fp.Paths, l.tracks[id].Path)
		}
		f.Playlists = append(f.Playlists, fp)
	}
	for _, id := range l.liked {
		f.Liked = append(f.Liked, l.tracks[id].Path)
	}
	b, err := json.Marshal(f)
	if err != nil {
		return
	}
	if os.MkdirAll(filepath.Dir(l.file), 0o755) != nil {
		return
	}
	tmp := l.file + ".tmp"
	if os.WriteFile(tmp, b, 0o644) == nil {
		_ = os.Rename(tmp, l.file)
	}
}

func (l *Library) sortedByPath() []*Track {
	out := make([]*Track, 0, len(l.tracks))
	for _, t := range l.tracks {
		out = append(out, t)
	}
	slices.SortFunc(out, func(a, b *Track) int { return strings.Compare(a.ID, b.ID) })
	return out
}

// ---- adding, scanning, removing ----

type fileEntry struct {
	path    string
	modTime int64
	size    int64
}

func statEntry(path string) (fileEntry, bool) {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return fileEntry{}, false
	}
	return fileEntry{path, fi.ModTime().UnixNano(), fi.Size()}, true
}

// underRoot reports whether id lies inside a watched folder.
func (l *Library) underRoot(id string) bool {
	for _, r := range l.folders {
		rid := ID(r)
		if strings.HasPrefix(id, rid+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// AddPaths adds audio files and folders (watched from now on; walked in the background). It returns the IDs of
// the files added directly, in order, so they can be played at once.
func (l *Library) AddPaths(paths []string) []string {
	var ids []string
	var entries []fileEntry
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		fi, err := os.Stat(abs)
		if err != nil {
			continue
		}
		if fi.IsDir() {
			l.addFolder(abs)
			continue
		}
		if formats.IsAudio(abs) {
			id := ID(abs)
			delete(l.excluded, id)
			entries = append(entries, fileEntry{abs, fi.ModTime().UnixNano(), fi.Size()})
			ids = append(ids, id)
		}
	}
	l.upsert(entries)
	return ids
}

func (l *Library) addFolder(abs string) {
	id := ID(abs)
	for _, r := range l.folders {
		rid := ID(r)
		if rid == id || strings.HasPrefix(id, rid+string(filepath.Separator)) {
			l.walk(r) // already watched (or inside a watched folder): just rescan
			return
		}
	}
	// A new root replaces the roots it contains.
	l.folders = slices.DeleteFunc(l.folders, func(r string) bool {
		return strings.HasPrefix(ID(r), id+string(filepath.Separator))
	})
	l.folders = append(l.folders, abs)
	for eid := range l.excluded {
		if strings.HasPrefix(eid, id+string(filepath.Separator)) {
			delete(l.excluded, eid)
		}
	}
	l.walk(abs)
	l.changed()
}

// Rescan walks every watched folder and checks the individually added files.
func (l *Library) Rescan() {
	for _, r := range l.folders {
		l.walk(r)
	}
	var loose []string
	for _, t := range l.tracks {
		if !l.underRoot(t.ID) {
			loose = append(loose, t.Path)
		}
	}
	l.walking++
	go func() {
		var found []fileEntry
		var missing []string
		for _, p := range loose {
			if e, ok := statEntry(p); ok {
				found = append(found, e)
			} else {
				missing = append(missing, ID(p))
			}
		}
		l.loop.Post(func() {
			l.walking--
			l.upsert(found)
			l.remove(missing, false)
			l.changed()
		})
	}()
}

func (l *Library) walk(root string) {
	l.walking++
	l.changed()
	go func() {
		var found []fileEntry
		_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !formats.IsAudio(p) {
				return nil
			}
			if fi, err := d.Info(); err == nil {
				found = append(found, fileEntry{p, fi.ModTime().UnixNano(), fi.Size()})
			}
			return nil
		})
		l.loop.Post(func() { l.reconcile(root, found) })
	}()
}

// reconcile applies a walk of root: new and changed files are (re)scanned, vanished ones leave the library.
func (l *Library) reconcile(root string, found []fileEntry) {
	l.walking--
	rid := ID(root)
	seen := make(map[string]bool, len(found))
	var keep []fileEntry
	for _, e := range found {
		id := ID(e.path)
		seen[id] = true
		if !l.excluded[id] {
			keep = append(keep, e)
		}
	}
	var gone []string
	for id := range l.tracks {
		if strings.HasPrefix(id, rid+string(filepath.Separator)) && !seen[id] {
			gone = append(gone, id)
		}
	}
	l.upsert(keep)
	l.remove(gone, false)
	l.changed()
}

// upsert adds new files and rescans changed ones.
func (l *Library) upsert(entries []fileEntry) {
	var jobs []metadata.ScanJob
	for _, e := range entries {
		id := ID(e.path)
		t := l.tracks[id]
		if t != nil && t.ModTime == e.modTime && t.Size == e.size && !t.Pending {
			continue
		}
		if t == nil {
			t = &Track{ID: id, Path: e.path, Info: metadata.Placeholder(e.path)}
			l.tracks[id] = t
		}
		t.ModTime, t.Size, t.Pending = e.modTime, e.size, true
		l.next++
		if t.token != 0 {
			delete(l.byToken, t.token)
		}
		t.token = l.next
		l.byToken[t.token] = t
		jobs = append(jobs, metadata.ScanJob{Token: t.token, Path: t.Path})
	}
	if len(jobs) > 0 {
		if l.scanner != nil {
			l.scanner.Scan(jobs)
		}
		l.changed()
	}
}

// Refresh rereads a track's tags (after they were edited).
func (l *Library) Refresh(id string) {
	t := l.tracks[id]
	if t == nil {
		return
	}
	if e, ok := statEntry(t.Path); ok {
		t.ModTime = 0 // force
		l.upsert([]fileEntry{e})
	}
}

// OnTagsReady applies scan results.
func (l *Library) OnTagsReady(batch []metadata.TagResult) {
	for _, r := range batch {
		t := l.byToken[r.Token]
		if t == nil {
			continue // removed meanwhile
		}
		delete(l.byToken, r.Token)
		t.token = 0
		t.Info, t.HasCover, t.HasLyrics, t.Pending = r.Info, r.HasCover, r.HasLyrics, false
	}
	l.changed()
}

// Remove takes tracks out of the library (and its playlists). Files inside watched folders stay out on rescans.
func (l *Library) Remove(ids []string) {
	l.remove(ids, true)
	l.changed()
}

func (l *Library) remove(ids []string, exclude bool) {
	if len(ids) == 0 {
		return
	}
	gone := map[string]bool{}
	cancel := map[uint64]bool{}
	for _, id := range ids {
		t := l.tracks[id]
		if t == nil {
			continue
		}
		gone[id] = true
		if t.token != 0 {
			cancel[t.token] = true
			delete(l.byToken, t.token)
		}
		delete(l.tracks, id)
		if exclude && l.underRoot(id) {
			l.excluded[id] = true
		}
	}
	if len(gone) == 0 {
		return
	}
	if l.scanner != nil && len(cancel) > 0 {
		l.scanner.Cancel(cancel)
	}
	for _, pl := range l.lists {
		pl.IDs = slices.DeleteFunc(pl.IDs, func(id string) bool { return gone[id] })
	}
	l.liked = slices.DeleteFunc(l.liked, func(id string) bool { return gone[id] })
	for id := range gone {
		delete(l.likedSet, id)
	}
	if l.OnRemoved != nil {
		removed := make([]string, 0, len(gone))
		for id := range gone {
			removed = append(removed, id)
		}
		l.OnRemoved(removed)
	}
}

// ---- views ----

func lower(s string) string { return strings.ToLower(s) }

// compareText orders case-insensitively, with unknown ("") values last.
func compareText(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == "":
		return 1
	case b == "":
		return -1
	}
	return strings.Compare(lower(a), lower(b))
}

func compareAlbumOrder(a, b *Track) int {
	if c := a.Info.DiscNumber - b.Info.DiscNumber; c != 0 {
		return c
	}
	if c := a.Info.TrackNumber - b.Info.TrackNumber; c != 0 {
		return c
	}
	return compareText(a.Info.Title, b.Info.Title)
}

func albumKey(t *Track) string { return lower(t.ArtistName()) + "\x00" + lower(t.Info.Album) }

func (l *Library) build() *views {
	if l.view.rev == l.rev && l.view.songs != nil {
		return &l.view
	}
	v := views{rev: l.rev, albumByKey: map[string]*Album{}, artistByKey: map[string]*Artist{},
		folderByPath: map[string]*Folder{}}
	v.songs = make([]*Track, 0, len(l.tracks))
	for _, t := range l.tracks {
		v.songs = append(v.songs, t)
	}
	// Songs: by artist, album, disc, track.
	slices.SortFunc(v.songs, func(a, b *Track) int {
		if c := compareText(a.ArtistName(), b.ArtistName()); c != 0 {
			return c
		}
		if c := compareText(a.Info.Album, b.Info.Album); c != 0 {
			return c
		}
		if c := compareAlbumOrder(a, b); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	for _, t := range v.songs {
		k := albumKey(t)
		al := v.albumByKey[k]
		if al == nil {
			al = &Album{Key: k, Title: t.Info.Album, Artist: t.ArtistName()}
			v.albumByKey[k] = al
			v.albums = append(v.albums, al)
		}
		al.Tracks = append(al.Tracks, t)
		if t.Info.Year > al.Year {
			al.Year = t.Info.Year
		}
		ak := lower(t.ArtistName())
		ar := v.artistByKey[ak]
		if ar == nil {
			ar = &Artist{Key: ak, Name: t.ArtistName()}
			v.artistByKey[ak] = ar
			v.artists = append(v.artists, ar)
		}
		ar.Tracks = append(ar.Tracks, t)
		dir := filepath.Dir(t.Path)
		fk := lower(dir)
		fo := v.folderByPath[fk]
		if fo == nil {
			fo = &Folder{Path: dir}
			v.folderByPath[fk] = fo
			v.folders = append(v.folders, fo)
		}
		fo.Tracks = append(fo.Tracks, t)
	}
	for _, al := range v.albums {
		slices.SortStableFunc(al.Tracks, compareAlbumOrder)
	}
	slices.SortFunc(v.albums, func(a, b *Album) int {
		if c := compareText(a.Title, b.Title); c != 0 {
			return c
		}
		return compareText(a.Artist, b.Artist)
	})
	for _, al := range v.albums {
		v.artistByKey[lower(al.Artist)].Albums++
	}
	slices.SortFunc(v.artists, func(a, b *Artist) int { return compareText(a.Name, b.Name) })
	slices.SortFunc(v.folders, func(a, b *Folder) int { return strings.Compare(lower(a.Path), lower(b.Path)) })
	for _, fo := range v.folders {
		slices.SortStableFunc(fo.Tracks, func(a, b *Track) int { return strings.Compare(a.ID, b.ID) })
	}
	l.view = v
	return &l.view
}

func (l *Library) Songs() []*Track            { return l.build().songs }
func (l *Library) Albums() []*Album           { return l.build().albums }
func (l *Library) Album(key string) *Album    { return l.build().albumByKey[key] }
func (l *Library) Artists() []*Artist         { return l.build().artists }
func (l *Library) Artist(key string) *Artist  { return l.build().artistByKey[key] }
func (l *Library) FolderList() []*Folder      { return l.build().folders }
func (l *Library) Folder(path string) *Folder { return l.build().folderByPath[lower(path)] }
func (l *Library) AlbumOf(t *Track) *Album    { return l.build().albumByKey[albumKey(t)] }
func (l *Library) ArtistOf(t *Track) *Artist  { return l.build().artistByKey[lower(t.ArtistName())] }
func (l *Library) Resolve(ids []string) []*Track {
	out := make([]*Track, 0, len(ids))
	for _, id := range ids {
		if t := l.tracks[id]; t != nil {
			out = append(out, t)
		}
	}
	return out
}

// Search returns the songs whose title, artist, album artist or album contain every word of query.
func (l *Library) Search(query string) []*Track {
	words := strings.Fields(lower(query))
	if len(words) == 0 {
		return nil
	}
	var out []*Track
	for _, t := range l.Songs() {
		hay := lower(t.Info.Title + "\x00" + t.Info.Artist + "\x00" + t.Info.AlbumArtist + "\x00" + t.Info.Album)
		ok := true
		for _, w := range words {
			if !strings.Contains(hay, w) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, t)
		}
	}
	return out
}

// TotalDuration sums the tracks' durations in ms.
func TotalDuration(ts []*Track) int64 {
	var d int64
	for _, t := range ts {
		d += t.Info.DurationMs
	}
	return d
}

// IDs returns the tracks' IDs.
func IDs(ts []*Track) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.ID
	}
	return out
}

// ---- playlists and likes ----

func (l *Library) Playlists() []*Playlist { return l.lists }

func (l *Library) Playlist(name string) *Playlist {
	for _, pl := range l.lists {
		if pl.Name == name {
			return pl
		}
	}
	return nil
}

// uniqueName returns name, or "name 2", "name 3", ... if taken.
func (l *Library) uniqueName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "New Playlist"
	}
	if l.Playlist(name) == nil {
		return name
	}
	for i := 2; ; i++ {
		if n := name + " " + strconv.Itoa(i); l.Playlist(n) == nil {
			return n
		}
	}
}

// NewPlaylist creates a playlist (with a unique name) holding ids, and returns its name.
func (l *Library) NewPlaylist(name string, ids []string) string {
	pl := &Playlist{Name: l.uniqueName(name), IDs: l.valid(ids)}
	l.lists = append(l.lists, pl)
	l.changed()
	return pl.Name
}

func (l *Library) valid(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if l.tracks[id] != nil {
			out = append(out, id)
		}
	}
	return out
}

// AddToPlaylist inserts ids at index at (-1 = the end).
func (l *Library) AddToPlaylist(name string, ids []string, at int) {
	pl := l.Playlist(name)
	if pl == nil {
		return
	}
	ids = l.valid(ids)
	if at < 0 || at > len(pl.IDs) {
		at = len(pl.IDs)
	}
	pl.IDs = slices.Insert(pl.IDs, at, ids...)
	l.changed()
}

// RemoveFromPlaylist removes the entries at the given indexes.
func (l *Library) RemoveFromPlaylist(name string, indexes []int) {
	pl := l.Playlist(name)
	if pl == nil {
		return
	}
	drop := map[int]bool{}
	for _, i := range indexes {
		drop[i] = true
	}
	out := pl.IDs[:0]
	for i, id := range pl.IDs {
		if !drop[i] {
			out = append(out, id)
		}
	}
	pl.IDs = out
	l.changed()
}

// MoveInPlaylist moves the entries at indexes (in order) so they start at index to (in the list without them).
func (l *Library) MoveInPlaylist(name string, indexes []int, to int) {
	pl := l.Playlist(name)
	if pl == nil {
		return
	}
	pl.IDs = moveItems(pl.IDs, indexes, to)
	l.changed()
}

// moveItems removes the items at indexes and reinserts them, in order, at position to of the remaining list.
func moveItems(list []string, indexes []int, to int) []string {
	pick := map[int]bool{}
	for _, i := range indexes {
		pick[i] = true
	}
	var moved, rest []string
	for i, v := range list {
		if pick[i] {
			moved = append(moved, v)
		} else {
			rest = append(rest, v)
		}
	}
	to = max(0, min(to, len(rest)))
	return slices.Insert(rest, to, moved...)
}

func (l *Library) DeletePlaylist(name string) {
	l.lists = slices.DeleteFunc(l.lists, func(pl *Playlist) bool { return pl.Name == name })
	l.changed()
}

func (l *Library) RenamePlaylist(name, to string) string {
	pl := l.Playlist(name)
	if pl == nil || strings.TrimSpace(to) == "" || to == name {
		return name
	}
	pl.Name = l.uniqueName(to)
	l.changed()
	return pl.Name
}

func (l *Library) Liked() []*Track        { return l.Resolve(l.liked) }
func (l *Library) IsLiked(id string) bool { return l.likedSet[id] }

func (l *Library) ToggleLike(id string) {
	if l.tracks[id] == nil {
		return
	}
	if l.likedSet[id] {
		delete(l.likedSet, id)
		l.liked = slices.DeleteFunc(l.liked, func(v string) bool { return v == id })
	} else {
		l.likedSet[id] = true
		l.liked = append([]string{id}, l.liked...) // newest first
	}
	l.changed()
}
