package metadata

/*
#cgo pkg-config: libavformat libavcodec libavutil
#include <stdlib.h>
#include "tagwriter.h"
*/
import "C"

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"unsafe"
)

// TagFields are the editable fields.
type TagFields struct {
	Title, Artist, Album, AlbumArtist, Genre string
	Year, TrackNumber, DiscNumber            int
}

// FieldsOf returns the editable fields of info.
func FieldsOf(info TrackInfo) TagFields {
	return TagFields{info.Title, info.Artist, info.Album, info.AlbumArtist, info.Genre, info.Year, info.TrackNumber,
		info.DiscNumber}
}

func numTag(v int) string {
	if v <= 0 {
		return ""
	}
	return strconv.Itoa(v)
}

// WriteTags rewrites path's tags. The audio is copied untouched (no re-encoding); the file is replaced only once
// the new copy is complete. The file must not be open for playback.
func WriteTags(path string, t TagFields) error {
	keys := []string{"title", "artist", "album", "album_artist", "genre", "date", "track", "disc"}
	values := []string{t.Title, t.Artist, t.Album, t.AlbumArtist, t.Genre, numTag(t.Year), numTag(t.TrackNumber),
		numTag(t.DiscNumber)}

	// The temporary copy keeps the extension, which selects the output container.
	ext := filepath.Ext(path)
	tmp := path[:len(path)-len(ext)] + ".mptags" + ext

	ck := make([]*C.char, len(keys))
	cv := make([]*C.char, len(keys))
	for i := range keys {
		ck[i] = C.CString(keys[i])
		cv[i] = C.CString(values[i])
		defer C.free(unsafe.Pointer(ck[i]))
		defer C.free(unsafe.Pointer(cv[i]))
	}
	cin, cout := C.CString(path), C.CString(tmp)
	defer C.free(unsafe.Pointer(cin))
	defer C.free(unsafe.Pointer(cout))
	cerr := (*C.char)(C.calloc(256, 1))
	defer C.free(unsafe.Pointer(cerr))

	// C arrays: cgo does not allow passing Go memory that holds C pointers across as **char.
	karr := (**C.char)(C.calloc(C.size_t(len(keys)), C.size_t(unsafe.Sizeof(uintptr(0)))))
	varr := (**C.char)(C.calloc(C.size_t(len(keys)), C.size_t(unsafe.Sizeof(uintptr(0)))))
	defer C.free(unsafe.Pointer(karr))
	defer C.free(unsafe.Pointer(varr))
	copy(unsafe.Slice(karr, len(keys)), ck)
	copy(unsafe.Slice(varr, len(keys)), cv)

	if r := C.mp_write_tags(cin, cout, karr, varr, C.int(len(keys)), cerr, 256); r < 0 {
		os.Remove(tmp)
		return errors.New("could not write tags: " + C.GoString(cerr))
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
