// Package metadata reads tags, audio properties, cover art and lyrics (through FFmpeg's demuxers: ID3v2, Vorbis
// comments, FLAC PICTURE blocks, MP4 atoms, RIFF INFO), decodes and caches cover thumbnails, and runs all of it
// off the UI goroutine (Service).
package metadata

/*
#cgo pkg-config: libavformat libavcodec libavutil
#include <stdlib.h>
#include <libavformat/avformat.h>
#include <libavutil/dict.h>

static int64_t mp_nopts(void) { return AV_NOPTS_VALUE; }
static int64_t mp_time_base(void) { return AV_TIME_BASE; }
static AVStream *mp_stream(AVFormatContext *f, unsigned i) { return f->streams[i]; }
static const AVDictionaryEntry *mp_dict_next(const AVDictionary *m, const AVDictionaryEntry *prev) {
	return av_dict_get(m, "", prev, AV_DICT_IGNORE_SUFFIX);
}
*/
import "C"

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unsafe"

	"musicplayer/internal/formats"
)

type ReplayGain struct {
	TrackGain, TrackPeak, AlbumGain, AlbumPeak float64 // dB / linear; NaN if absent
}

type TrackInfo struct {
	Title       string // never empty: falls back to the file name
	Artist      string
	Album       string
	AlbumArtist string
	Genre       string
	Year        int
	TrackNumber int
	DiscNumber  int
	DurationMs  int64
	SampleRate  int    // Hz
	Bitrate     int    // kbps
	Format      string // "FLAC", "MP3", ...
	Lossless    bool
	ReplayGain  ReplayGain
}

// Placeholder is what a row shows before its tags are read: file name as title, format badge from the extension.
// No I/O.
func Placeholder(path string) TrackInfo {
	base := filepath.Base(path)
	return TrackInfo{
		Title:      strings.TrimSuffix(base, filepath.Ext(base)),
		Format:     formats.Badge(path),
		Lossless:   formats.IsLossless(path),
		ReplayGain: ReplayGain{math.NaN(), math.NaN(), math.NaN(), math.NaN()},
	}
}

// Part selects what Extract reads.
type Part int

const (
	Tags          Part = 1 << iota // tags, audio properties, ReplayGain
	CoverPresence                  // HasCover only
	Cover                          // the embedded cover's encoded bytes (implies presence)
	Lyrics                         // full lyrics text (otherwise only HasLyrics)
)

type Extraction struct {
	Info      TrackInfo
	HasCover  bool // embedded picture present
	HasLyrics bool
	CoverData []byte // encoded embedded picture (Cover)
	Lyrics    string // LRC or plain text (Lyrics)
}

var noPTS = int64(C.mp_nopts())

func readDict(d *C.AVDictionary, into map[string]string) {
	var e *C.AVDictionaryEntry
	for {
		e = (*C.AVDictionaryEntry)(unsafe.Pointer(C.mp_dict_next(d, e)))
		if e == nil {
			return
		}
		k := strings.ToLower(C.GoString(e.key))
		if _, seen := into[k]; !seen {
			into[k] = C.GoString(e.value)
		}
	}
}

// leadingInt parses the digits at the start of s ("3/12" -> 3, "2019-05-01" -> 2019).
func leadingInt(s string) int {
	s = strings.TrimSpace(s)
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	v, _ := strconv.Atoi(s[:end])
	return v
}

// gainValue parses "-6.52 dB" / "0.988"; NaN if absent or unparsable.
func gainValue(tags map[string]string, key string) float64 {
	s, ok := tags[key]
	if !ok {
		return math.NaN()
	}
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(s), "dB"), "db"))
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return math.NaN()
	}
	return v
}

// lyricsFromTags picks the lyrics from the merged tag map: ID3 USLT ("lyrics-eng"), Vorbis / MP4 "LYRICS", and the
// common non-standard user-text variants.
func lyricsFromTags(tags map[string]string) string {
	for _, k := range []string{"lyrics"} {
		if v := tags[k]; strings.TrimSpace(v) != "" {
			return v
		}
	}
	for k, v := range tags {
		if strings.HasPrefix(k, "lyrics-") && strings.TrimSpace(v) != "" {
			return v
		}
	}
	for _, k := range []string{"unsyncedlyrics", "syncedlyrics", "uslt"} {
		if v := tags[k]; strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// Extract opens path once and returns everything asked for. Unreadable files yield the placeholder info. Safe for
// concurrent use; meant for worker goroutines.
func Extract(path string, parts Part) (ex Extraction) {
	ex = Extraction{Info: Placeholder(path)}
	title := ex.Info.Title
	ex.Info.Title = ""
	defer func() {
		if ex.Info.Title == "" {
			ex.Info.Title = title
		}
	}()

	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	var fc *C.AVFormatContext
	if C.avformat_open_input(&fc, cpath, nil, nil) < 0 {
		return ex
	}
	defer C.avformat_close_input(&fc)

	// Pick the audio stream; header-only probing is enough for every supported container except raw ADTS, whose
	// duration and rate need a look at the packets.
	audio := C.av_find_best_stream(fc, C.AVMEDIA_TYPE_AUDIO, -1, -1, nil, 0)
	needInfo := audio < 0
	if audio >= 0 {
		st := C.mp_stream(fc, C.uint(audio))
		needInfo = st.codecpar.sample_rate <= 0 || (int64(fc.duration) == noPTS && int64(st.duration) == noPTS)
	}
	if needInfo && parts&Tags != 0 {
		C.avformat_find_stream_info(fc, nil)
		audio = C.av_find_best_stream(fc, C.AVMEDIA_TYPE_AUDIO, -1, -1, nil, 0)
	}

	tags := map[string]string{}
	readDict(fc.metadata, tags)
	if audio >= 0 {
		readDict(C.mp_stream(fc, C.uint(audio)).metadata, tags) // Ogg keeps its comments on the stream
	}

	if parts&Tags != 0 {
		ex.Info.Title = strings.TrimSpace(tags["title"])
		ex.Info.Artist = strings.TrimSpace(tags["artist"])
		ex.Info.Album = strings.TrimSpace(tags["album"])
		ex.Info.Genre = strings.TrimSpace(tags["genre"])
		if y := leadingInt(tags["date"]); y > 0 {
			ex.Info.Year = y
		} else {
			ex.Info.Year = leadingInt(tags["year"])
		}
		ex.Info.TrackNumber = leadingInt(tags["track"])
		ex.Info.DiscNumber = leadingInt(tags["disc"])
		ex.Info.AlbumArtist = strings.TrimSpace(tags["album_artist"])

		if audio >= 0 {
			st := C.mp_stream(fc, C.uint(audio))
			seconds := 0.0
			if int64(fc.duration) != noPTS && fc.duration > 0 {
				seconds = float64(fc.duration) / float64(C.mp_time_base())
			} else if int64(st.duration) != noPTS && st.duration > 0 {
				seconds = float64(st.duration) * float64(st.time_base.num) / float64(st.time_base.den)
			}
			ex.Info.DurationMs = int64(seconds * 1000)
			ex.Info.SampleRate = int(st.codecpar.sample_rate)
			bitrate := int64(st.codecpar.bit_rate)
			if bitrate <= 0 {
				bitrate = int64(fc.bit_rate)
			}
			if bitrate <= 0 && seconds > 0 {
				if fi, err := os.Stat(path); err == nil {
					bitrate = int64(float64(fi.Size()*8) / seconds)
				}
			}
			ex.Info.Bitrate = int(bitrate / 1000)
		}
		ex.Info.ReplayGain = ReplayGain{
			gainValue(tags, "replaygain_track_gain"), gainValue(tags, "replaygain_track_peak"),
			gainValue(tags, "replaygain_album_gain"), gainValue(tags, "replaygain_album_peak"),
		}
	}

	// Lyrics: an ID3v2 SYLT frame keeps its timing, so it wins where present (MP3 / AAC); then the tag text.
	id3 := formats.Badge(path) == "MP3" || formats.Badge(path) == "AAC"
	lyricsText := ""
	if id3 && parts&Lyrics != 0 {
		lyricsText = SyncedLyricsFromID3(path)
	}
	if lyricsText == "" {
		lyricsText = lyricsFromTags(tags)
	}
	if lyricsText == "" && id3 && parts&Lyrics == 0 {
		lyricsText = SyncedLyricsFromID3(path)
	}
	ex.HasLyrics = strings.TrimSpace(lyricsText) != ""
	if parts&Lyrics != 0 {
		ex.Lyrics = lyricsText
	}

	// Cover: attached-picture streams (APIC, FLAC / Vorbis PICTURE, MP4 covr). Front cover preferred.
	if parts&(Cover|CoverPresence) != 0 {
		var fallback []byte
		for i := C.uint(0); i < fc.nb_streams; i++ {
			st := C.mp_stream(fc, i)
			if st.disposition&C.AV_DISPOSITION_ATTACHED_PIC == 0 || st.attached_pic.size <= 0 {
				continue
			}
			ex.HasCover = true
			if parts&Cover == 0 {
				break
			}
			comment := map[string]string{}
			readDict(st.metadata, comment)
			front := strings.EqualFold(comment["comment"], "Cover (front)")
			if front || fallback == nil {
				fallback = C.GoBytes(unsafe.Pointer(st.attached_pic.data), st.attached_pic.size)
			}
			if front {
				break
			}
		}
		if parts&Cover != 0 {
			ex.CoverData = fallback
			ex.HasCover = len(fallback) > 0
		}
	}
	return ex
}
