package email

import (
	"context"
	"strings"
	"testing"
)

func TestSMTPSenderBuildsMessage(t *testing.T) {
	s := NewSMTPSender("smtp.example.com", 587, "user", "pass", "no-reply@example.com", "Gregport")

	msg := s.buildMessage("customer@example.com", TemplateTicketCreated, DeliveryPayload{
		EventType:     "ticket.created",
		OrgName:       "Acme",
		TicketNumber:  1042,
		Subject:       "Cannot log in",
		RequesterName: "Ada Customer",
		AssigneeName:  "Grace Agent",
		EventData:     []byte(`{"number":1042}`),
	})

	if !strings.Contains(msg, "From: Gregport <no-reply@example.com>") {
		t.Fatalf("from: %q", msg)
	}
	if !strings.Contains(msg, "To: customer@example.com") {
		t.Fatalf("to: %q", msg)
	}
	if !strings.Contains(msg, "Subject: [ticket_created] Cannot log in") {
		t.Fatalf("subject: %q", msg)
	}
	if !strings.Contains(msg, "Event: ticket.created") {
		t.Fatalf("event: %q", msg)
	}
	if !strings.Contains(msg, "Org: Acme") {
		t.Fatalf("org: %q", msg)
	}
	if !strings.Contains(msg, "Ticket #1042: Cannot log in") {
		t.Fatalf("ticket: %q", msg)
	}
	if !strings.Contains(msg, "Requester: Ada Customer") {
		t.Fatalf("requester: %q", msg)
	}
	if !strings.Contains(msg, "Assignee: Grace Agent") {
		t.Fatalf("assignee: %q", msg)
	}
	if !strings.Contains(msg, `{"number":1042}`) {
		t.Fatalf("event data: %q", msg)
	}
}

func TestSMTPSenderBuildsMessageWithoutFromName(t *testing.T) {
	s := NewSMTPSender("smtp.example.com", 587, "", "", "no-reply@example.com", "")

	msg := s.buildMessage("customer@example.com", TemplateTicketAssigned, DeliveryPayload{
		EventType:    "ticket.assigned",
		OrgName:      "Acme",
		TicketNumber: 5,
		Subject:      "You have a new assignee",
	})

	if !strings.Contains(msg, "From: no-reply@example.com") {
		t.Fatalf("from: %q", msg)
	}
}

func TestSMTPSenderSendFailsOnBadAddress(t *testing.T) {
	s := NewSMTPSender("127.0.0.1", 9, "", "", "no-reply@example.com", "Gregport")

	err := s.Send(context.Background(), "customer@example.com", TemplateTicketCreated, DeliveryPayload{})
	if err == nil {
		t.Fatal("expected an error connecting to a closed port")
	}
}
