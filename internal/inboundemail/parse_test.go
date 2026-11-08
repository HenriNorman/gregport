package inboundemail

import (
	"strings"
	"testing"
)

func TestParseExtractsHeadersAndBody(t *testing.T) {
	raw := []byte(
		"From: Ada Lovelace <ada@example.test>\n" +
			"To: support+93f9e1c0-1234-4a56-8b9c-0123456789ab@inbound.example.test\n" +
			"Message-ID: <msg-1@example.test>\n" +
			"Subject: [#1042] Cannot log in\n" +
			"In-Reply-To: <orig@example.test>\n" +
			"\n" +
			"This is the reply body.\n")
	e, err := Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if e.MessageID != "msg-1@example.test" {
		t.Fatalf("message id=%q", e.MessageID)
	}
	if e.InReplyTo != "orig@example.test" {
		t.Fatalf("in reply to=%q", e.InReplyTo)
	}
	if e.FromEmail != "ada@example.test" {
		t.Fatalf("from email=%q", e.FromEmail)
	}
	if e.FromName != "Ada Lovelace" {
		t.Fatalf("from name=%q", e.FromName)
	}
	if e.Recipient != "support+93f9e1c0-1234-4a56-8b9c-0123456789ab@inbound.example.test" {
		t.Fatalf("recipient=%q", e.Recipient)
	}
	if e.Subject != "[#1042] Cannot log in" {
		t.Fatalf("subject=%q", e.Subject)
	}
	if !strings.Contains(e.Body, "This is the reply body.") {
		t.Fatalf("body=%q", e.Body)
	}
	if len(e.Raw) == 0 {
		t.Fatal("raw not stored")
	}
}

func TestParseMessageIDCanonicalized(t *testing.T) {
	raw := []byte(
		"From: a@example.test\n" +
			"To: b@example.test\n" +
			"Message-ID: <  spaced@example.test >\n" +
			"\n" +
			"body\n")
	e, err := Parse(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if e.MessageID != "spaced@example.test" {
		t.Fatalf("message id=%q", e.MessageID)
	}
}

func TestParseNoMessageID(t *testing.T) {
	raw := []byte("From: a@example.test\nTo: b@example.test\nSubject: hi\n\nbody\n")
	if _, err := Parse(raw); err == nil {
		t.Fatal("expected error for missing Message-ID")
	}
}

func TestTicketNumberFromSubject(t *testing.T) {
	cases := []struct {
		subject string
		want    int64
	}{
		{"[#1042] Cannot log in", 1042},
		{"Re: [#1042] Cannot log in", 1042},
		{"[#1] hi", 1},
		{"hello world", 0},
		{"[#abc] not a number", 0},
		{"[] empty", 0},
	}
	for _, c := range cases {
		if got := TicketNumberFromSubject(c.subject); got != c.want {
			t.Fatalf("subject=%q got=%d want=%d", c.subject, got, c.want)
		}
	}
}
