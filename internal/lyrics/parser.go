// Package lyrics parses synchronised (LRC) lyrics and decodes lyric files of unknown encoding.
package lyrics

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// WordSpan is one sung word (enhanced LRC "<mm:ss.xx>word"). OffsetMs is relative to the line's timestamp, so a
// line that appears at several times ("[00:12.00][01:40.50] chorus") shares its words. A span with empty text
// marks where the previous word ends (a trailing "<mm:ss.xx>").
type WordSpan struct {
	OffsetMs int64
	Text     string
}

type Line struct {
	TimestampMs int64
	Text        string     // the whole line, word tags removed
	Words       []WordSpan // empty for plain line-synced LRC
}

var (
	lineRe    = regexp.MustCompile(`^\s*((?:\[\d{1,3}:\d{2}(?:[.:]\d{1,3})?\]\s*)+)(.*)$`)
	tagRe     = regexp.MustCompile(`\[(\d{1,3}):(\d{2})(?:[.:](\d{1,3}))?\]`)
	offsetRe  = regexp.MustCompile(`(?i)^\s*\[offset:\s*([+-]?\d{1,9})\s*\]`)
	wordTagRe = regexp.MustCompile(`<(\d{1,3}):(\d{2})(?:[.:](\d{1,3}))?>`)
	newlineRe = regexp.MustCompile("\r\n|\r|\n")
)

// toMs converts the minute, second and optional fraction captures of a time tag.
func toMs(min, sec, frac string) int64 {
	m, _ := strconv.ParseInt(min, 10, 64)
	s, _ := strconv.ParseInt(sec, 10, 64)
	ms := m*60000 + s*1000
	if frac != "" {
		v, _ := strconv.ParseInt(frac, 10, 64)
		switch len(frac) {
		case 1:
			v *= 100 // .5  -> 500 ms
		case 2:
			v *= 10 // .50 -> 500 ms
		}
		ms += v
	}
	return ms
}

// Parse parses LRC text: "[mm:ss.xx] text", several tags per line ("[00:12.00][01:40.50] chorus"), 1-3 fraction
// digits, "[offset:+/-ms]", enhanced word tags (<mm:ss.xx>, kept as WordSpans), and ignores metadata tags
// ([ar:], [ti:], ...). The result is sorted by time. Text without any time tag (plain, unsynchronised lyrics)
// yields an empty list. Any input, however malformed, is safe.
func Parse(text string) []Line {
	var lines []Line
	var offsetMs int64

	for _, row := range newlineRe.Split(text, -1) {
		if off := offsetRe.FindStringSubmatch(row); off != nil {
			offsetMs, _ = strconv.ParseInt(off[1], 10, 64)
			continue
		}
		m := lineRe.FindStringSubmatch(row)
		if m == nil {
			continue // metadata tag, blank line, or plain text
		}

		// Split the text at its word tags: text before the first tag is sung from the line start, each tag
		// starts the text up to the next one.
		raw := m[2]
		var lead string
		type timedText struct {
			at   int64
			text string
		}
		var timed []timedText
		pos := 0
		for _, loc := range wordTagRe.FindAllStringSubmatchIndex(raw, -1) {
			seg := raw[pos:loc[0]]
			if len(timed) == 0 {
				lead = seg
			} else {
				timed[len(timed)-1].text += seg
			}
			frac := ""
			if loc[6] >= 0 {
				frac = raw[loc[6]:loc[7]]
			}
			timed = append(timed, timedText{toMs(raw[loc[2]:loc[3]], raw[loc[4]:loc[5]], frac), ""})
			pos = loc[1]
		}
		if len(timed) == 0 {
			lead = raw
		} else {
			timed[len(timed)-1].text += raw[pos:]
		}

		var sb strings.Builder
		sb.WriteString(lead)
		for _, t := range timed {
			sb.WriteString(t.text)
		}
		lyric := strings.TrimSpace(sb.String())

		// Word offsets are relative to the first time tag of the line, never negative, never going backwards.
		var starts []int64
		for _, t := range tagRe.FindAllStringSubmatch(m[1], -1) {
			starts = append(starts, toMs(t[1], t[2], t[3]))
		}
		var words []WordSpan
		if len(timed) > 0 && len(starts) > 0 {
			if strings.TrimSpace(lead) != "" {
				words = append(words, WordSpan{0, lead})
			}
			var last int64
			for _, t := range timed {
				rel := max(last, t.at-starts[0])
				words = append(words, WordSpan{rel, t.text})
				last = rel
			}
		}
		// Per the LRC spec a positive offset makes lyrics appear earlier.
		for _, start := range starts {
			lines = append(lines, Line{max(0, start-offsetMs), lyric, words})
		}
	}

	sort.SliceStable(lines, func(i, j int) bool { return lines[i].TimestampMs < lines[j].TimestampMs })
	return lines
}

// ActiveIndexAt returns the index of the line active at positionMs (the last line whose timestamp <= position),
// or -1 before the first line.
func ActiveIndexAt(lines []Line, positionMs int64) int {
	// First line strictly after the position; the one before it is active.
	return sort.Search(len(lines), func(i int) bool { return positionMs < lines[i].TimestampMs }) - 1
}

// ActiveWordAt returns the index of the word of line being sung at positionMs (the last word whose start <=
// position), or -1 before its first word or when the line has no word timing.
func ActiveWordAt(line Line, positionMs int64) int {
	rel := positionMs - line.TimestampMs
	return sort.Search(len(line.Words), func(i int) bool { return rel < line.Words[i].OffsetMs }) - 1
}
