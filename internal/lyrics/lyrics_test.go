package lyrics

import (
	"encoding/binary"
	"math/rand"
	"testing"
	"unicode/utf16"
)

func TestParse(t *testing.T) {
	lines := Parse("[offset:+100]\n" +
		"[00:10.00][01:10.00]Hey <00:10.50>there <00:11.00>you<00:11.40>\n" +
		"[00:20.00]plain line\n")
	if len(lines) != 3 {
		t.Fatalf("%d lines", len(lines))
	}
	a := lines[0]
	if a.TimestampMs != 9900 || a.Text != "Hey there you" {
		t.Errorf("line 0: %d %q", a.TimestampMs, a.Text)
	}
	// "Hey " is sung from the line start; the trailing tag marks where "you" ends.
	want := []WordSpan{{0, "Hey "}, {500, "there "}, {1000, "you"}, {1400, ""}}
	if len(a.Words) != len(want) {
		t.Fatalf("%d words", len(a.Words))
	}
	for i, w := range want {
		if a.Words[i] != w {
			t.Errorf("word %d = %+v, want %+v", i, a.Words[i], w)
		}
	}
	if ActiveWordAt(a, 9900+499) != 0 || ActiveWordAt(a, 9900+500) != 1 || ActiveWordAt(a, 9899) != -1 {
		t.Error("ActiveWordAt")
	}
	if lines[1].Text != "plain line" || len(lines[1].Words) != 0 {
		t.Errorf("line 1: %+v", lines[1])
	}
	if lines[2].TimestampMs != 69900 || len(lines[2].Words) != 4 {
		t.Error("repeated line keeps its words")
	}
	if ActiveIndexAt(lines, 0) != -1 || ActiveIndexAt(lines, 9900) != 0 || ActiveIndexAt(lines, 25000) != 1 {
		t.Error("ActiveIndexAt")
	}
}

func TestDecode(t *testing.T) {
	jp, zh, fr := "こんにちは世界", "你好世界", "Café déjà vu"
	utf16le := func(s string) []byte {
		b := []byte{0xFF, 0xFE}
		for _, u := range utf16.Encode([]rune(s)) {
			b = binary.LittleEndian.AppendUint16(b, u)
		}
		return b
	}
	cases := []struct {
		name     string
		bytes    []byte
		expected string
	}{
		{"UTF-8", []byte("[00:01.00]" + fr), "[00:01.00]" + fr},
		{"UTF-8 + BOM", append([]byte("\xEF\xBB\xBF[00:01.00]"), jp...), "[00:01.00]" + jp},
		{"UTF-16LE + BOM", utf16le("[00:01.00]" + zh), "[00:01.00]" + zh},
		{"Shift-JIS", []byte("[00:01.00]\x82\xb1\x82\xf1\x82\xc9\x82\xbf\x82\xcd\x90\xa2\x8a\x45"), "[00:01.00]" + jp},
		{"GBK", []byte("[00:01.00]\xc4\xe3\xba\xc3\xca\xc0\xbd\xe7"), "[00:01.00]" + zh},
		{"Windows-1252", []byte("[00:01.00]Caf\xe9 d\xe9j\xe0 vu"), "[00:01.00]" + fr},
	}
	for _, c := range cases {
		if got := Decode(c.bytes); got != c.expected {
			t.Errorf("%s decoded as %q", c.name, got)
		}
	}
}

func TestFuzz(t *testing.T) {
	rng := rand.New(rand.NewSource(12345))
	alphabet := []byte("[]<>:.0123456789+-offset \n\r\xEF\xBB\xBF\x82\xA0\xFF\xFE")
	// A valid word-timed LRC to mutate, so the line / word-tag paths run on broken input too.
	seed := []byte("[ar:x]\n[offset:-250]\n[00:01.00][00:30.5]Hey <00:01.50>there <00:02.00>you<00:02.40>\n" +
		"[00:03.000]<00:03.10>word <00:03.40>by <00:03.90>word\n[99:59.99]end\n" +
		"\xEF\xBB\xBF[00:04:12]\x82\xb1\x82\xf1\n")
	var lines, words int
	for run := 0; run < 30000; run++ {
		var b []byte
		if run%3 == 2 {
			b = append([]byte(nil), seed...)
			for m := rng.Intn(13); m >= 0; m-- {
				at := 0
				if len(b) > 0 {
					at = rng.Intn(len(b))
				}
				switch rng.Intn(4) {
				case 0:
					if len(b) > 0 {
						b[at] = byte(rng.Intn(256))
					}
				case 1:
					b = append(b[:at], append([]byte{alphabet[rng.Intn(len(alphabet))]}, b[at:]...)...)
				case 2:
					end := min(len(b), at+1+rng.Intn(8))
					b = append(b[:at], b[end:]...)
				case 3:
					b = b[:at]
				}
			}
		} else {
			b = make([]byte, rng.Intn(2048))
			for i := range b {
				if run%3 == 1 {
					b[i] = alphabet[rng.Intn(len(alphabet))]
				} else {
					b[i] = byte(rng.Intn(256))
				}
			}
		}
		parsed := Parse(Decode(b))
		lines += len(parsed)
		for _, l := range parsed {
			words += len(l.Words)
			ActiveWordAt(l, l.TimestampMs+int64(rng.Intn(5000)))
			if l.TimestampMs < 0 {
				t.Fatal("negative timestamp")
			}
			for w := 1; w < len(l.Words); w++ {
				if l.Words[w].OffsetMs < l.Words[w-1].OffsetMs || l.Words[w].OffsetMs < 0 {
					t.Fatal("word offsets go backwards")
				}
			}
		}
		ActiveIndexAt(parsed, int64(rng.Intn(100000)))
	}
	if lines < 10000 || words < 10000 {
		t.Errorf("fuzzing barely reached the parser (%d lines, %d words)", lines, words)
	}
}
