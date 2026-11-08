package inboundemail

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"testing"
	"time"
)

func TestVerifySignature(t *testing.T) {
	secret := "0123456789012345678901234567890123456789012345678901234567890123"
	timestamp := "1700000000"
	body := []byte(`{"message":"hello"}`)
	sig := signForTest(secret, timestamp, body)

	if !VerifySignature(secret, timestamp, body, []byte(sig)) {
		t.Fatal("valid signature rejected")
	}
	if VerifySignature(secret, timestamp, body, []byte("deadbeef")) {
		t.Fatal("invalid signature accepted")
	}
	if VerifySignature("other", timestamp, body, []byte(sig)) {
		t.Fatal("wrong secret accepted")
	}
	if VerifySignature(secret, timestamp, []byte(`{"message":"hello2"}`), []byte(sig)) {
		t.Fatal("different body accepted")
	}
}

func TestVerifyTimestamp(t *testing.T) {
	now := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)

	// Within tolerance -> ok
	ts := now.Add(-time.Minute).Unix()
	if err := VerifyTimestamp(strconv.FormatInt(ts, 10), now, DefaultSignatureTolerance); err != nil {
		t.Fatalf("recent timestamp rejected: %v", err)
	}

	// Outside tolerance -> expired
	ts = now.Add(-10 * time.Minute).Unix()
	if !errors.Is(VerifyTimestamp(strconv.FormatInt(ts, 10), now, DefaultSignatureTolerance), ErrExpiredSignature) {
		t.Fatal("old timestamp accepted")
	}

	// Empty -> expired
	if !errors.Is(VerifyTimestamp("", now, DefaultSignatureTolerance), ErrExpiredSignature) {
		t.Fatal("empty timestamp accepted")
	}
}

func signForTest(secret, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
