package attachments

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestSanitizeFilename(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"receipt.pdf", "receipt.pdf", false},
		{"../../etc/passwd", "passwd", false},
		{`C:\Windows\system32\evil.exe`, "evil.exe", false},
		{"a\x00b.txt", "ab.txt", false},
		{"  spaced.txt  ", "spaced.txt", false},
		{"", "", true},
		{"..", "", true},
		{".hidden", "", true},
		{strings.Repeat("a", 300) + ".txt", "", true},
	}
	for _, c := range cases {
		got, err := SanitizeFilename(c.in)
		if c.wantErr {
			if !errors.Is(err, ErrInvalidFilename) {
				t.Fatalf("in=%q err=%v want invalid filename", c.in, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("in=%q err=%v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("in=%q got=%q want=%q", c.in, got, c.want)
		}
	}
}

func TestNormalizeMIMEType(t *testing.T) {
	if got, err := NormalizeMIMEType("IMAGE/PNG"); err != nil || got != "image/png" {
		t.Fatalf("got=%q err=%v", got, err)
	}
	if _, err := NormalizeMIMEType("application/x-msdownload"); !errors.Is(err, ErrUnsupportedType) {
		t.Fatalf("exe accepted: %v", err)
	}
	if _, err := NormalizeMIMEType("not a mime"); !errors.Is(err, ErrUnsupportedType) {
		t.Fatalf("garbage accepted: %v", err)
	}
}

func TestContentDisposition(t *testing.T) {
	if got := ContentDisposition("application/pdf", `a"b.pdf`); !strings.HasPrefix(got, "attachment") || !strings.Contains(got, `a\"b.pdf`) {
		t.Fatalf("got=%q", got)
	}
	if got := ContentDisposition("image/png", "a.png"); !strings.HasPrefix(got, "inline") {
		t.Fatalf("got=%q", got)
	}
}

func TestObjectKeyNeverContainsFilename(t *testing.T) {
	key := ObjectKey(uuid.MustParse("11111111-1111-1111-1111-111111111111"), uuid.MustParse("22222222-2222-2222-2222-222222222222"), uuid.MustParse("33333333-3333-3333-3333-333333333333"))
	want := "org/11111111-1111-1111-1111-111111111111/tickets/22222222-2222-2222-2222-222222222222/33333333-3333-3333-3333-333333333333"
	if key != want {
		t.Fatalf("key=%q want=%q", key, want)
	}
}
