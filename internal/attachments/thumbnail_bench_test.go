package attachments

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"testing"

	"github.com/disintegration/imaging"
)

// BenchmarkThumbnailParallel measures the CPU-bound thumbnail pipeline:
// decode a realistic image, fit it to a bounded edge and JPEG-encode it.
// Run with several -cpu values to compare throughput at different worker
// concurrency levels:
//
//	go test ./internal/attachments -run '^$' -bench BenchmarkThumbnailParallel -cpu 1,2,4,8
func BenchmarkThumbnailParallel(b *testing.B) {
	src := benchmarkJPEG(b)
	b.ReportAllocs()
	b.SetBytes(int64(len(src)))
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			img, err := imaging.Decode(bytes.NewReader(src))
			if err != nil {
				b.Fatal(err)
			}
			thumb := imaging.Fit(img, 512, 512, imaging.Lanczos)
			var buf bytes.Buffer
			if err := imaging.Encode(&buf, thumb, imaging.JPEG, imaging.JPEGQuality(85)); err != nil {
				b.Fatal(err)
			}
		}
	})
}

func benchmarkJPEG(b *testing.B) []byte {
	b.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1600, 1200))
	for y := 0; y < 1200; y++ {
		for x := 0; x < 1600; x++ {
			img.Set(x, y, color.RGBA{
				R: uint8(x % 256),
				G: uint8(y % 256),
				B: uint8((x + y) % 256),
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90}); err != nil {
		b.Fatalf("encode benchmark image: %v", err)
	}
	return buf.Bytes()
}
