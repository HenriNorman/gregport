package inboundemail

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
)

func serviceFixture(t *testing.T) (*Service, *fakeRepo, uuid.UUID) {
	t.Helper()
	repo := newFakeRepo()
	orgID := uuid.New()
	repo.seedOrg(orgID)
	svc := NewService(repo, "0123456789012345678901234567890123456789012345678901234567890123")
	return svc, repo, orgID
}

func signedBody(t *testing.T, secret string, body []byte) (timestamp, signature string) {
	t.Helper()
	timestamp = strconv.FormatInt(time.Now().UTC().Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	signature = hex.EncodeToString(mac.Sum(nil))
	return
}

func makeBody(msgID, recipient, subject, extra, body string) []byte {
	return []byte("From: Ada <ada@example.test>\n" +
		"To: " + recipient + "\n" +
		"Message-ID: <" + msgID + ">\n" +
		subject + "\n" +
		extra + "\n\n" +
		body + "\n")
}

func TestServiceProcessCreatesTicket(t *testing.T) {
	svc, repo, orgID := serviceFixture(t)
	recipient := orgID.String() + "@inbound.example.test"
	msg := makeBody("m-create-1", recipient, "Subject: hello", "", "body text")
	timestamp, signature := signedBody(t, svc.secret, msg)

	err := svc.Process(context.Background(), msg, timestamp, signature)
	if err != nil {
		t.Fatalf("process: %v", err)
	}

	last := repo.lastInsert()
	if last.Status != "ticket_created" {
		t.Fatalf("status=%s want ticket_created", last.Status)
	}
	if last.TicketID == nil {
		t.Fatal("no ticket id recorded")
	}
	if len(repo.comments) != 1 {
		t.Fatalf("comments=%d", len(repo.comments))
	}
}

func TestServiceProcessCommentBySubject(t *testing.T) {
	svc, repo, orgID := serviceFixture(t)
	ticketID := repo.seedTicket(orgID, 1042)

	recipient := orgID.String() + "@inbound.example.test"
	msg := makeBody("m-comment-subject-1", recipient, "Subject: Re: [#1042] hello", "", "reply body")
	timestamp, signature := signedBody(t, svc.secret, msg)

	if err := svc.Process(context.Background(), msg, timestamp, signature); err != nil {
		t.Fatalf("process: %v", err)
	}

	if len(repo.comments) != 1 || repo.comments[0].TicketID != ticketID {
		t.Fatalf("comment not added to ticket: %+v", repo.comments)
	}
	last := repo.lastInsert()
	if last.Status != "comment_added" || last.TicketID == nil || *last.TicketID != ticketID {
		t.Fatalf("last insert: %+v", last)
	}
}

func TestServiceProcessCommentByInReplyTo(t *testing.T) {
	svc, repo, orgID := serviceFixture(t)
	ticketID := repo.seedTicket(orgID, 1042)
	origMsgID := "orig-1@example.test"
	// Seed the original inbound email that the reply references.
	repo.seedEmail(orgID, InboundEmailRow{
		OrgID:     orgID,
		MessageID: origMsgID,
		TicketID:  &ticketID,
		Status:    "ticket_created",
	})

	recipient := orgID.String() + "@inbound.example.test"
	msg := makeBody("m-comment-reply-1", recipient, "Subject: Re: hello", "In-Reply-To: <"+origMsgID+">", "reply body")
	timestamp, signature := signedBody(t, svc.secret, msg)

	if err := svc.Process(context.Background(), msg, timestamp, signature); err != nil {
		t.Fatalf("process: %v", err)
	}

	if len(repo.comments) != 1 || repo.comments[0].TicketID != ticketID {
		t.Fatalf("comment not added to ticket: %+v", repo.comments)
	}
}

func TestServiceProcessDedupeMessageID(t *testing.T) {
	svc, repo, orgID := serviceFixture(t)
	recipient := orgID.String() + "@inbound.example.test"
	msg := makeBody("m-dedupe-1", recipient, "Subject: hi", "", "body")
	timestamp, signature := signedBody(t, svc.secret, msg)

	if err := svc.Process(context.Background(), msg, timestamp, signature); err != nil {
		t.Fatalf("first process: %v", err)
	}
	// Same body again -> idempotent, no error, no new ticket/comment.
	if err := svc.Process(context.Background(), msg, timestamp, signature); err != nil {
		t.Fatalf("second process should be idempotent: %v", err)
	}
	if len(repo.comments) != 1 {
		t.Fatalf("duplicate changed comment count: %+v", repo.comments)
	}
	if len(repo.insertCalls) != 1 {
		t.Fatalf("insertCalls=%d want 1 (no duplicate insert)", len(repo.insertCalls))
	}
	last := repo.lastInsert()
	if last.Status != "ticket_created" {
		t.Fatalf("status=%s want ticket_created", last.Status)
	}
}

func TestServiceProcessRejectsBadSignature(t *testing.T) {
	svc, _, orgID := serviceFixture(t)
	recipient := orgID.String() + "@inbound.example.test"
	msg := makeBody("m-badsig-1", recipient, "Subject: hi", "", "body")
	timestamp, _ := signedBody(t, svc.secret, msg)

	if err := svc.Process(context.Background(), msg, timestamp, "badsignature"); !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("err=%v want ErrInvalidSignature", err)
	}
}

func TestServiceProcessRejectsExpiredTimestamp(t *testing.T) {
	svc, _, orgID := serviceFixture(t)
	recipient := orgID.String() + "@inbound.example.test"
	msg := makeBody("m-expired-1", recipient, "Subject: hi", "", "body")
	old := strconv.FormatInt(time.Now().UTC().Add(-10*time.Minute).Unix(), 10)
	mac := hmac.New(sha256.New, []byte(svc.secret))
	mac.Write([]byte(old))
	mac.Write([]byte("."))
	mac.Write(msg)
	signature := hex.EncodeToString(mac.Sum(nil))

	if err := svc.Process(context.Background(), msg, old, signature); !errors.Is(err, ErrExpiredSignature) {
		t.Fatalf("err=%v want ErrExpiredSignature", err)
	}
}

func TestServiceProcessUnknownOrg(t *testing.T) {
	svc, _, _ := serviceFixture(t)
	recipient := uuid.New().String() + "@inbound.example.test"
	msg := makeBody("m-unknownorg-1", recipient, "Subject: hi", "", "body")
	timestamp, signature := signedBody(t, svc.secret, msg)

	if err := svc.Process(context.Background(), msg, timestamp, signature); !errors.Is(err, ErrOrgNotFound) {
		t.Fatalf("err=%v want ErrOrgNotFound", err)
	}
}

func TestServiceProcessMissingSecret(t *testing.T) {
	repo := newFakeRepo()
	orgID := uuid.New()
	repo.seedOrg(orgID)
	svc := NewService(repo, "")
	recipient := orgID.String() + "@inbound.example.test"
	msg := makeBody("m-nosecret-1", recipient, "Subject: hi", "", "body")

	if err := svc.Process(context.Background(), msg, "1", "sig"); err == nil {
		t.Fatal("expected error for empty secret")
	}
}

func TestServiceProcessInReplyToUnknownMessageCreatesTicket(t *testing.T) {
	svc, repo, orgID := serviceFixture(t)
	recipient := orgID.String() + "@inbound.example.test"
	msg := makeBody("m-unknownreply-1", recipient, "Subject: new issue", "In-Reply-To: <never-seen@example.test>", "body")
	timestamp, signature := signedBody(t, svc.secret, msg)

	if err := svc.Process(context.Background(), msg, timestamp, signature); err != nil {
		t.Fatalf("process: %v", err)
	}
	last := repo.lastInsert()
	if last.Status != "ticket_created" {
		t.Fatalf("status=%s want ticket_created", last.Status)
	}
}
