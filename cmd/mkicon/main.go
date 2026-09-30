// Command mkicon writes the application icon as a multi-size .ico (PNG-compressed entries), for the executable's
// resources. Usage: go run ./cmd/mkicon <out.ico>
package main

import (
	"bytes"
	"encoding/binary"
	"image/png"
	"log"
	"os"

	"musicplayer/internal/win"
)

func main() {
	if len(os.Args) != 2 {
		log.Fatal("usage: mkicon <out.ico>")
	}
	sizes := []int{16, 24, 32, 48, 64, 256}
	var images [][]byte
	for _, s := range sizes {
		var b bytes.Buffer
		if err := png.Encode(&b, win.AppIconImage(s)); err != nil {
			log.Fatal(err)
		}
		images = append(images, b.Bytes())
	}
	var out bytes.Buffer
	le := func(v any) { binary.Write(&out, binary.LittleEndian, v) }
	le(uint16(0))          // reserved
	le(uint16(1))          // type: icon
	le(uint16(len(sizes))) // count
	offset := 6 + 16*len(sizes)
	for i, s := range sizes {
		dim := uint8(s)
		if s >= 256 {
			dim = 0 // 0 means 256
		}
		out.Write([]byte{dim, dim, 0, 0})
		le(uint16(1))  // planes
		le(uint16(32)) // bits per pixel
		le(uint32(len(images[i])))
		le(uint32(offset))
		offset += len(images[i])
	}
	for _, img := range images {
		out.Write(img)
	}
	if err := os.WriteFile(os.Args[1], out.Bytes(), 0o644); err != nil {
		log.Fatal(err)
	}
}
