// Package imaging resizes uploads and produces pixelated thumbnails.
package imaging

import (
	"bytes"
	"image"
	"image/jpeg"
	_ "image/png" // register PNG decoder

	"golang.org/x/image/draw"
)

// Prepare decodes JPEG/PNG, downscales to ≤1024px and re-encodes (dropping metadata).
func Prepare(raw []byte) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if longest := max(w, h); longest > 1024 {
		w, h = w*1024/longest, h*1024/longest
	}
	dst := image.NewRGBA(image.Rect(0, 0, max(w, 1), max(h, 1)))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Src, nil)
	return encode(dst, 85)
}

// BlurThumb shrinks to 16px wide then scales up to 240px with nearest-neighbour.
func BlurThumb(raw []byte) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	sh := max(b.Dy()*16/max(b.Dx(), 1), 1)
	small := image.NewRGBA(image.Rect(0, 0, 16, sh))
	draw.ApproxBiLinear.Scale(small, small.Bounds(), src, b, draw.Src, nil)
	big := image.NewRGBA(image.Rect(0, 0, 240, sh*15))
	draw.NearestNeighbor.Scale(big, big.Bounds(), small, small.Bounds(), draw.Src, nil)
	return encode(big, 70)
}

func encode(img image.Image, q int) ([]byte, error) {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: q}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
