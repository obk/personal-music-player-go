package lyrics

import (
	"bytes"
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/encoding/unicode/utf32"
)

// Decode returns the text of a lyrics file of unknown encoding: BOM (UTF-8/16/32) if present, else strict UTF-8,
// else the most plausible of Shift-JIS, GBK and Windows-1252 (which accepts anything).
func Decode(b []byte) string {
	// 1. A byte-order mark decides. UTF-32 is checked first: its little-endian BOM starts like UTF-16's.
	switch {
	case bytes.HasPrefix(b, []byte{0xEF, 0xBB, 0xBF}):
		return string(b[3:])
	case bytes.HasPrefix(b, []byte{0xFF, 0xFE, 0x00, 0x00}):
		return decodeWith(utf32.UTF32(utf32.LittleEndian, utf32.ExpectBOM), b)
	case bytes.HasPrefix(b, []byte{0x00, 0x00, 0xFE, 0xFF}):
		return decodeWith(utf32.UTF32(utf32.BigEndian, utf32.ExpectBOM), b)
	case bytes.HasPrefix(b, []byte{0xFF, 0xFE}):
		return decodeWith(unicode.UTF16(unicode.LittleEndian, unicode.ExpectBOM), b)
	case bytes.HasPrefix(b, []byte{0xFE, 0xFF}):
		return decodeWith(unicode.UTF16(unicode.BigEndian, unicode.ExpectBOM), b)
	}
	// 2. Valid UTF-8 (which includes plain ASCII) is taken as such.
	if utf8.Valid(b) {
		return string(b)
	}
	// 3. Legacy code pages: the most plausible strict decode of Shift-JIS / GBK, else Windows-1252.
	sjis, sjisOK := strictDecode(japanese.ShiftJIS, b)
	gbk, gbkOK := strictDecode(simplifiedchinese.GBK, b)
	sjisScore, gbkScore := -1, -1
	if sjisOK {
		s := classify(sjis)
		sjisScore = 3*s.kana + s.cjk + s.cjkPunct - 3*s.halfKana - 2*s.other
	}
	if gbkOK {
		s := classify(gbk)
		gbkScore = s.cjk + s.cjkPunct + s.kana - 2*s.halfKana - 2*s.other
	}
	if sjisScore > 0 && sjisScore > gbkScore {
		return sjis
	}
	if gbkScore > 0 {
		return gbk
	}
	return decodeWith(charmap.Windows1252, b)
}

func decodeWith(e encoding.Encoding, b []byte) string {
	out, err := e.NewDecoder().Bytes(b)
	if err != nil {
		return string(b)
	}
	return string(out)
}

// strictDecode decodes b, failing on any invalid sequence (which x/text replaces with U+FFFD or SUB).
func strictDecode(e encoding.Encoding, b []byte) (string, bool) {
	out, err := e.NewDecoder().Bytes(b)
	if err != nil {
		return "", false
	}
	s := string(out)
	for _, r := range s {
		if r == utf8.RuneError || r == 0x1A {
			return "", false
		}
	}
	return s, true
}

// script counts how much a text looks like Japanese (Shift-JIS) or Chinese (GBK). Mis-decoded bytes typically
// show up as half-width katakana (Chinese read as Shift-JIS) or as ideographs mixed with nothing else.
type script struct {
	kana     int // hiragana + full-width katakana
	halfKana int // half-width katakana
	cjk      int // ideographs
	cjkPunct int // CJK symbols / full-width forms
	other    int // any other non-ASCII
}

func classify(text string) script {
	var s script
	for _, u := range text {
		switch {
		case u < 0x80:
		case u >= 0x3040 && u <= 0x30FF:
			s.kana++
		case u >= 0xFF66 && u <= 0xFF9F:
			s.halfKana++
		case (u >= 0x4E00 && u <= 0x9FFF) || (u >= 0x3400 && u <= 0x4DBF):
			s.cjk++
		case (u >= 0x3000 && u <= 0x303F) || (u >= 0xFF00 && u <= 0xFFEF):
			s.cjkPunct++
		default:
			s.other++
		}
	}
	return s
}
