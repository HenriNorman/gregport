package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
)

func TestParseLevel(t *testing.T) {
	tests := []struct {
		in      string
		want    slog.Level
		wantErr bool
	}{
		{in: "", want: slog.LevelInfo},
		{in: "info", want: slog.LevelInfo},
		{in: "INFO", want: slog.LevelInfo},
		{in: "debug", want: slog.LevelDebug},
		{in: "warn", want: slog.LevelWarn},
		{in: "warning", want: slog.LevelWarn},
		{in: "error", want: slog.LevelError},
		{in: "trace", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseLevel(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNew_InvalidLevel(t *testing.T) {
	if _, err := New("loud"); err == nil {
		t.Fatal("expected error for invalid level")
	}
}

func TestNewWithWriter_EmitsJSON(t *testing.T) {
	var buf bytes.Buffer
	l := NewWithWriter(&buf, slog.LevelInfo)
	l.Info("hello", "answer", 42)

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("not valid JSON: %v (%q)", err, buf.String())
	}
	if got["level"] != "INFO" || got["msg"] != "hello" {
		t.Fatalf("unexpected entry: %+v", got)
	}
	if got["answer"] != float64(42) {
		t.Fatalf("missing attr: %+v", got)
	}
}

func TestFromContext_DefaultAndRoundTrip(t *testing.T) {
	if FromContext(context.Background()) == nil {
		t.Fatal("expected non-nil default logger")
	}

	var buf bytes.Buffer
	l := NewWithWriter(&buf, slog.LevelInfo)
	ctx := WithLogger(context.Background(), l)
	FromContext(ctx).Info("scoped")

	if !strings.Contains(buf.String(), `"msg":"scoped"`) {
		t.Fatalf("scoped logger did not write: %q", buf.String())
	}
}
