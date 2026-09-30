package player

import "musicplayer/internal/lyrics"

// lyricsState selects the active line and word of synchronised lyrics from the playback position.
type lyricsState struct {
	lines    []lyrics.Line
	active   int
	word     int
	offsetMs int // per-track correction; positive shows lyrics earlier
	lastPos  int64
}

func (l *lyricsState) setLines(lines []lyrics.Line) {
	l.lines = lines
	l.active, l.word = -1, -1
	l.onPosition(l.lastPos) // e.g. lyrics arriving after playback already started
}

func (l *lyricsState) clear() {
	l.lines = nil
	l.active, l.word = -1, -1
}

func (l *lyricsState) onPosition(ms int64) {
	l.lastPos = ms
	if len(l.lines) == 0 {
		return
	}
	t := ms + int64(l.offsetMs)
	l.active = lyrics.ActiveIndexAt(l.lines, t)
	l.word = -1
	if l.active >= 0 {
		l.word = lyrics.ActiveWordAt(l.lines[l.active], t)
	}
}
