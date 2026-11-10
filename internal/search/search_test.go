package search

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestRenderHighlightEscapesSourceAndRestoresMarks(t *testing.T) {
	raw := "Login <script>alert(1)</script> " + hlStart + "failure" + hlEnd + " & more"
	got := RenderHighlight(raw)

	if strings.Contains(got, "<script>") {
		t.Fatalf("highlight contains unescaped script tag: %q", got)
	}
	if !strings.Contains(got, "&lt;script&gt;") {
		t.Fatalf("highlight did not escape source markup: %q", got)
	}
	if !strings.Contains(got, "<mark>failure</mark>") {
		t.Fatalf("highlight did not restore mark markers: %q", got)
	}
	if !strings.Contains(got, "&amp;") {
		t.Fatalf("highlight did not escape ampersand: %q", got)
	}
}

func TestRenderHighlightEmpty(t *testing.T) {
	if got := RenderHighlight(""); got != "" {
		t.Fatalf("empty highlight = %q, want empty", got)
	}
}

func TestCursorRoundTrip(t *testing.T) {
	id := uuid.New()
	created := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	raw := encodeCursor(0.125, created, id)

	c, err := decodeCursor(raw)
	if err != nil {
		t.Fatalf("decode cursor: %v", err)
	}
	if c.Rank != 0.125 || !c.CreatedAt.Equal(created) || c.ID != id {
		t.Fatalf("cursor round trip = %+v", c)
	}
}

func TestDecodeCursorAcceptsEmpty(t *testing.T) {
	c, err := decodeCursor("")
	if err != nil {
		t.Fatalf("empty cursor: %v", err)
	}
	if c.ID != uuid.Nil || !c.CreatedAt.IsZero() {
		t.Fatalf("empty cursor = %+v", c)
	}
}

func TestDecodeCursorRejectsGarbage(t *testing.T) {
	for _, raw := range []string{"not-base64!", "e30", "eyJ0IjoiMjAyNi0wMS0xNVQxMDowMDowMFoiLCJpIjoiNTUwZTg0MDAtIn0"} {
		if _, err := decodeCursor(raw); err == nil {
			t.Fatalf("decodeCursor(%q) = nil error, want error", raw)
		}
	}
}

func TestNormalizeLimit(t *testing.T) {
	cases := map[int]int{
		0:    DefaultLimit,
		-1:   DefaultLimit,
		1:    1,
		25:   25,
		100:  100,
		101:  MaxLimit,
		1000: MaxLimit,
	}
	for in, want := range cases {
		if got := NormalizeLimit(in); got != want {
			t.Fatalf("NormalizeLimit(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestValidStatus(t *testing.T) {
	for _, s := range []string{"new", "open", "pending", "solved", "closed"} {
		if !ValidStatus(s) {
			t.Fatalf("ValidStatus(%q) = false", s)
		}
	}
	for _, s := range []string{"", "OPEN", "archived"} {
		if ValidStatus(s) {
			t.Fatalf("ValidStatus(%q) = true", s)
		}
	}
}
