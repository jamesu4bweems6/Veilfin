// Package imaging 负责把各种来源的图片统一缩放并转成 JPEG。
package imaging

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/jpeg"

	_ "image/gif"
	_ "image/png"

	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// MaxDecodePixels 防止超大图片（解压炸弹）耗尽内存。
const MaxDecodePixels = 50_000_000

// 各类图片的最大尺寸
var Limits = map[string][2]int{
	"poster":   {600, 900},
	"backdrop": {1920, 1080},
	"thumb":    {960, 540},
}

// Normalize 解码图片，按比例缩小到 maxW×maxH 以内（不放大），输出 JPEG。
func Normalize(data []byte, maxW, maxH int) ([]byte, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > MaxDecodePixels {
		return nil, errors.New("图片尺寸异常")
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	scale := min(float64(maxW)/float64(w), float64(maxH)/float64(h), 1)
	nw, nh := max(int(float64(w)*scale), 1), max(int(float64(h)*scale), 1)
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	// 透明背景填充为深色，避免 PNG 透明区域变黑块之外的杂色
	draw.Draw(dst, dst.Bounds(), &image.Uniform{color.RGBA{16, 16, 16, 255}}, image.Point{}, draw.Src)
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)
	var out bytes.Buffer
	if err := jpeg.Encode(&out, dst, &jpeg.Options{Quality: 85}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// CropPoster 从横版封面中裁出右侧竖版海报（日本影片封面的常见布局：右半部分是正面）。
func CropPoster(data []byte) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= h {
		return Normalize(data, Limits["poster"][0], Limits["poster"][1])
	}
	cw := int(float64(h) * 0.71) // 约 2:3 再略宽一点，贴近实际包装比例
	if cw > w {
		cw = w
	}
	rect := image.Rect(b.Max.X-cw, b.Min.Y, b.Max.X, b.Max.Y)
	dst := image.NewRGBA(image.Rect(0, 0, cw, h))
	draw.Draw(dst, dst.Bounds(), src, rect.Min, draw.Src)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 92}); err != nil {
		return nil, err
	}
	return Normalize(buf.Bytes(), Limits["poster"][0], Limits["poster"][1])
}

// CropCenter 从图片中间裁出宽高比为 ratio（宽/高）的区域，用于把横版截图做成竖版海报。
func CropCenter(data []byte, ratio float64) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	cw, ch := w, h
	if float64(w)/float64(h) > ratio {
		cw = int(float64(h) * ratio)
	} else {
		ch = int(float64(w) / ratio)
	}
	x0 := b.Min.X + (w-cw)/2
	y0 := b.Min.Y + (h-ch)/2
	dst := image.NewRGBA(image.Rect(0, 0, cw, ch))
	draw.Draw(dst, dst.Bounds(), src, image.Pt(x0, y0), draw.Src)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 92}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
