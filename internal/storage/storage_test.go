package storage

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestNewValidatesConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
	}{
		{"missing endpoint", Config{Bucket: "b", AccessKey: "a", SecretKey: "s"}},
		{"missing bucket", Config{Endpoint: "localhost:9000", AccessKey: "a", SecretKey: "s"}},
		{"missing credentials", Config{Endpoint: "localhost:9000", Bucket: "b"}},
	}
	for _, c := range cases {
		if _, err := New(c.cfg); err == nil {
			t.Fatalf("%s: expected error", c.name)
		}
	}
}

func testStore(t *testing.T) *S3 {
	t.Helper()
	s, err := New(Config{
		Endpoint:  "127.0.0.1:9000",
		AccessKey: "minioadmin",
		SecretKey: "minioadmin",
		Bucket:    "gregport-attachments",
		Region:    "us-east-1",
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	return s
}

func TestPresignPutIsScopedToObject(t *testing.T) {
	s := testStore(t)
	u, err := s.PresignPut(context.Background(), "org/abc/tickets/def/1234", 5*time.Minute)
	if err != nil {
		t.Fatalf("presign put: %v", err)
	}
	for _, want := range []string{"gregport-attachments", "org/abc/tickets/def/1234", "X-Amz-Signature"} {
		if !strings.Contains(u, want) {
			t.Fatalf("url %q missing %q", u, want)
		}
	}
}

func TestPresignGetSetsContentDisposition(t *testing.T) {
	s := testStore(t)
	u, err := s.PresignGet(context.Background(), "org/abc/tickets/def/1234", time.Minute,
		`attachment; filename="report.pdf"`)
	if err != nil {
		t.Fatalf("presign get: %v", err)
	}
	if !strings.Contains(u, "response-content-disposition") {
		t.Fatalf("url %q missing content disposition", u)
	}
}

func TestIsNotFound(t *testing.T) {
	if isNotFound(nil) {
		t.Fatal("nil is not not-found")
	}
}
