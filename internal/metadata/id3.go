package metadata

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf16"

	"golang.org/x/text/encoding/charmap"
)

// FFmpeg exposes ID3v2 text frames but not SYLT (synchronised lyrics), so that one frame is read here.

func syncsafe(b []byte) int {
	return int(b[0]&0x7f)<<21 | int(b[1]&0x7f)<<14 | int(b[2]&0x7f)<<7 | int(b[3]&0x7f)
}

// deunsync reverses ID3 unsynchronisation (0xFF 0x00 -> 0xFF).
func deunsync(b []byte) []byte {
	return bytes.ReplaceAll(b, []byte{0xFF, 0x00}, []byte{0xFF})
}

// SyncedLyricsFromID3 returns the first SYLT frame with millisecond timestamps of the file's leading ID3v2 tag as
// LRC text, or "".
func SyncedLyricsFromID3(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	var hdr [10]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil || string(hdr[:3]) != "ID3" {
		return ""
	}
	major, flags, size := hdr[3], hdr[5], syncsafe(hdr[6:10])
	if major < 3 || major > 4 || size <= 0 || size > 64<<20 {
		return ""
	}
	tag := make([]byte, size)
	if _, err := io.ReadFull(f, tag); err != nil {
		return ""
	}
	if major == 3 && flags&0x80 != 0 {
		tag = deunsync(tag)
	}
	pos := 0
	if flags&0x40 != 0 && len(tag) >= 4 { // extended header
		if major == 4 {
			pos = syncsafe(tag[:4])
		} else {
			pos = int(binary.BigEndian.Uint32(tag[:4])) + 4
		}
	}
	for pos+10 <= len(tag) {
		id := string(tag[pos : pos+4])
		if id[0] == 0 {
			break // padding
		}
		var fsize int
		if major == 4 {
			fsize = syncsafe(tag[pos+4 : pos+8])
		} else {
			fsize = int(binary.BigEndian.Uint32(tag[pos+4 : pos+8]))
		}
		fflags := binary.BigEndian.Uint16(tag[pos+8 : pos+10])
		start := pos + 10
		if fsize < 0 || start+fsize > len(tag) {
			break
		}
		body := tag[start : start+fsize]
		pos = start + fsize
		if id != "SYLT" {
			continue
		}
		if major == 4 {
			if fflags&0x0001 != 0 && len(body) >= 4 { // data length indicator
				body = body[4:]
			}
			if fflags&0x0002 != 0 {
				body = deunsync(body)
			}
			if fflags&0x000C != 0 { // compressed / encrypted
				continue
			}
		} else if fflags&0x00C0 != 0 {
			continue
		}
		if lrc := syltToLrc(body); lrc != "" {
			return lrc
		}
	}
	return ""
}

// splitTerminated splits b at the first string terminator of the given ID3 text encoding.
func splitTerminated(b []byte, enc byte) (text, rest []byte, ok bool) {
	if enc == 1 || enc == 2 { // UTF-16: a 2-byte aligned 0x0000
		for i := 0; i+1 < len(b); i += 2 {
			if b[i] == 0 && b[i+1] == 0 {
				return b[:i], b[i+2:], true
			}
		}
		return nil, nil, false
	}
	i := bytes.IndexByte(b, 0)
	if i < 0 {
		return nil, nil, false
	}
	return b[:i], b[i+1:], true
}

func decodeID3Text(b []byte, enc byte, bigEndian *bool) string {
	switch enc {
	case 0:
		s, _ := charmap.ISO8859_1.NewDecoder().Bytes(b)
		return string(s)
	case 3:
		return string(b)
	}
	be := enc == 2
	if bigEndian != nil && enc == 1 {
		be = *bigEndian
	}
	if len(b) >= 2 && enc == 1 {
		switch {
		case b[0] == 0xFF && b[1] == 0xFE:
			be, b = false, b[2:]
		case b[0] == 0xFE && b[1] == 0xFF:
			be, b = true, b[2:]
		}
		if bigEndian != nil {
			*bigEndian = be
		}
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		if be {
			u[i] = binary.BigEndian.Uint16(b[2*i:])
		} else {
			u[i] = binary.LittleEndian.Uint16(b[2*i:])
		}
	}
	return string(utf16.Decode(u))
}

// syltToLrc converts a SYLT frame body (encoding, language, timestamp format, content type, descriptor, then
// text + 32-bit time pairs) to LRC, if its timestamps are absolute milliseconds.
func syltToLrc(body []byte) string {
	if len(body) < 6 {
		return ""
	}
	enc, format := body[0], body[4]
	if format != 2 || enc > 3 { // 2 = absolute milliseconds
		return ""
	}
	_, rest, ok := splitTerminated(body[6:], enc) // content descriptor
	if !ok {
		return ""
	}
	bigEndian := false
	var sb strings.Builder
	for len(rest) > 0 {
		text, after, ok := splitTerminated(rest, enc)
		if !ok || len(after) < 4 {
			break
		}
		t := binary.BigEndian.Uint32(after[:4])
		rest = after[4:]
		fmt.Fprintf(&sb, "[%02d:%02d.%02d]%s\n", t/60000, t/1000%60, t%1000/10,
			strings.TrimSpace(decodeID3Text(text, enc, &bigEndian)))
	}
	return sb.String()
}
