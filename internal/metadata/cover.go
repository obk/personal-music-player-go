package metadata

import (
	"bytes"
	"container/list"
	"image"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"
)

// DecodeCover decodes an encoded picture and scales it to fit maxSide (0 = full size). Nil on failure.
func DecodeCover(data []byte, maxSide int) *image.RGBA {
	if len(data) == 0 {
		return nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	return Fit(img, maxSide)
}

// Fit returns img as RGBA scaled down (area-averaged, aspect kept) so its longer side is at most maxSide.
func Fit(img image.Image, maxSide int) *image.RGBA {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil
	}
	if maxSide <= 0 || (w <= maxSide && h <= maxSide) {
		if rgba, ok := img.(*image.RGBA); ok && b.Min == (image.Point{}) {
			return rgba
		}
		dst := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.Draw(dst, dst.Bounds(), img, b.Min, draw.Src)
		return dst
	}
	dw, dh := maxSide, maxSide
	if w > h {
		dh = max(1, h*maxSide/w)
	} else {
		dw = max(1, w*maxSide/h)
	}
	src, ok := img.(*image.RGBA)
	if !ok || b.Min != (image.Point{}) {
		src = image.NewRGBA(image.Rect(0, 0, w, h))
		draw.Draw(src, src.Bounds(), img, b.Min, draw.Src)
	}
	return boxScale(src, dw, dh)
}

// boxScale downsamples by averaging every source pixel under each destination pixel (fractional coverage at the
// edges), which gives clean thumbnails at any reduction factor.
func boxScale(src *image.RGBA, dw, dh int) *image.RGBA {
	sw, sh := src.Rect.Dx(), src.Rect.Dy()
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	sx := float64(sw) / float64(dw)
	sy := float64(sh) / float64(dh)
	acc := make([]float64, dw*4)
	for y := 0; y < dh; y++ {
		clear(acc)
		y0, y1 := float64(y)*sy, float64(y+1)*sy
		for syi := int(y0); syi < sh && float64(syi) < y1; syi++ {
			wy := min(y1, float64(syi+1)) - max(y0, float64(syi))
			row := src.Pix[syi*src.Stride:]
			for x := 0; x < dw; x++ {
				x0, x1 := float64(x)*sx, float64(x+1)*sx
				a := acc[x*4 : x*4+4]
				for sxi := int(x0); sxi < sw && float64(sxi) < x1; sxi++ {
					w := wy * (min(x1, float64(sxi+1)) - max(x0, float64(sxi)))
					p := row[sxi*4 : sxi*4+4]
					a[0] += w * float64(p[0])
					a[1] += w * float64(p[1])
					a[2] += w * float64(p[2])
					a[3] += w * float64(p[3])
				}
			}
		}
		norm := 1 / (sx * sy)
		out := dst.Pix[y*dst.Stride:]
		for i := 0; i < dw*4; i++ {
			out[i] = uint8(min(255, acc[i]*norm+0.5))
		}
	}
	return dst
}

// FolderCoverPath finds cover / folder / front .jpg/.jpeg/.png next to audioPath, or "".
func FolderCoverPath(audioPath string) string {
	dir := filepath.Dir(audioPath)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, name := range []string{"cover", "folder", "front"} {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			n := e.Name()
			ext := strings.ToLower(filepath.Ext(n))
			if ext != ".jpg" && ext != ".jpeg" && ext != ".png" {
				continue
			}
			if strings.EqualFold(strings.TrimSuffix(n, filepath.Ext(n)), name) {
				return filepath.Join(dir, n)
			}
		}
	}
	return ""
}

// CoverSize is a cached thumbnail size (longest side in pixels).
type CoverSize int

const (
	Small  CoverSize = 64
	Medium CoverSize = 256
)

// CoverCache is a thread-safe LRU of decoded cover thumbnails, bounded by bytes. Filled by the Service on worker
// goroutines, read on the loop (track change) and by the UI. Evicted entries are simply decoded again.
type CoverCache struct {
	mu     sync.Mutex
	budget int
	used   int
	lru    *list.List // front = most recently used
	items  map[string]*list.Element
}

type cacheEntry struct {
	key  string
	img  *image.RGBA
	cost int
}

func NewCoverCache(budgetBytes int) *CoverCache {
	return &CoverCache{budget: budgetBytes, lru: list.New(), items: map[string]*list.Element{}}
}

func cacheKey(path string, size CoverSize) string {
	return strconv.Itoa(int(size)) + "|" + path
}

func (c *CoverCache) Insert(path string, size CoverSize, img *image.RGBA) {
	if img == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	k := cacheKey(path, size)
	if e, ok := c.items[k]; ok {
		c.used -= e.Value.(*cacheEntry).cost
		c.lru.Remove(e)
	}
	cost := len(img.Pix)
	c.items[k] = c.lru.PushFront(&cacheEntry{k, img, cost})
	c.used += cost
	for c.used > c.budget && c.lru.Len() > 1 {
		last := c.lru.Back()
		ent := last.Value.(*cacheEntry)
		c.lru.Remove(last)
		delete(c.items, ent.key)
		c.used -= ent.cost
	}
}

// Find returns the cached image (and marks it most recently used), or nil.
func (c *CoverCache) Find(path string, size CoverSize) *image.RGBA {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e, ok := c.items[cacheKey(path, size)]; ok {
		c.lru.MoveToFront(e)
		return e.Value.(*cacheEntry).img
	}
	return nil
}
