package ui

import (
	"image"

	"gioui.org/op/paint"

	"musicplayer/internal/metadata"
)

// imageCache keeps GPU image ops (a new paint.ImageOp per frame would re-upload the texture) and down-scaled
// copies of large images (linear filtering alone aliases when shrinking by more than ~2x).
type imageCache struct {
	frame int
	ops   map[*image.RGBA]*cachedOp
	small map[scaleKey]*cachedImg
}

type cachedOp struct {
	op   paint.ImageOp
	used int
}

type scaleKey struct {
	src    *image.RGBA
	bucket int
}

type cachedImg struct {
	img  *image.RGBA
	used int
}

func newImageCache() *imageCache {
	return &imageCache{ops: map[*image.RGBA]*cachedOp{}, small: map[scaleKey]*cachedImg{}}
}

func (c *imageCache) op(img *image.RGBA) paint.ImageOp {
	e, ok := c.ops[img]
	if !ok {
		e = &cachedOp{op: paint.NewImageOp(img)}
		c.ops[img] = e
	}
	e.used = c.frame
	return e.op
}

// scaled returns img, or a cached copy scaled down to about targetPx (power-of-two buckets) when img is much larger.
func (c *imageCache) scaled(img *image.RGBA, targetPx int) *image.RGBA {
	bucket := 32
	for bucket < targetPx {
		bucket *= 2
	}
	b := img.Bounds()
	if max(b.Dx(), b.Dy()) <= bucket*3/2 {
		return img
	}
	k := scaleKey{img, bucket}
	e, ok := c.small[k]
	if !ok {
		e = &cachedImg{img: metadata.Fit(img, bucket)}
		c.small[k] = e
	}
	e.used = c.frame
	return e.img
}

// sweep forgets entries not used for a while.
func (c *imageCache) sweep() {
	c.frame++
	if c.frame%120 != 0 {
		return
	}
	for k, e := range c.ops {
		if c.frame-e.used > 240 {
			delete(c.ops, k)
		}
	}
	for k, e := range c.small {
		if c.frame-e.used > 240 {
			delete(c.small, k)
		}
	}
}
