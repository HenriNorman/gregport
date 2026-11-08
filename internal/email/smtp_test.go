package email

import (
	"context"
	"strings"
	"testing"
)

func TestSMTPSenderBuildsMultipartMessage(t *testing.T) {
	s := NewSMTPSender("smtp.example.com", 587, "user", "pass", "no-reply@example.com", "Gregport")

	msg := s.buildMessage("customer@example.com", TemplateCommentAdded, DeliveryPayload{
		EventType:     "comment.added",
		OrgName:       "Acme",
		TicketNumber:  1042,
		Subject:       "Cannot log in",
		RequesterName: "Ada Customer",
		AssigneeName:  "Grace Agent",
		CommentBody:   "We are on it",
		ReplyTo:       "support@acme.test",
	})

	for _, want := range []string{
		"From: Gregport <no-reply@example.com>",
		"To: customer@example.com",
		"Reply-To: support@acme.test",
		"Subject: [#1042] Cannot log in",
		"Content-Type: multipart/alternative; boundary=",
		`Content-Type: text/plain; charset="utf-8"`,
		`Content-Type: text/html; charset="utf-8"`,
		"Content-Transfer-Encoding: quoted-printable",
		"[#1042] Cannot log in",
		"We are on it",
	} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message missing %q:\n%s", want, msg)
		}
	}

	// Both a plain-text and an HTML alternative are present.
	if strings.Count(msg, "Content-Type: multipart/alternative") != 1 {
		t.Fatalf("expected exactly one multipart content type: %s", msg)
	}
	if !strings.Contains(msg, "</html>") {
		t.Fatalf("html part missing: %s", msg)
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
	if !strings.Contains(msg, "Subject: [#5] You have a new assignee") {
		t.Fatalf("subject: %q", msg)
	}
	if strings.Contains(msg, "Reply-To:") {
		t.Fatalf("unexpected reply-to: %q", msg)
	}
}

func TestSMTPSenderEscapesHTML(t *testing.T) {
	s := NewSMTPSender("smtp.example.com", 587, "", "", "no-reply@example.com", "Gregport")

	payload := DeliveryPayload{
		OrgName:      "Acme",
		TicketNumber: 1042,
		Subject:      `<b>Cannot</b> log in`,
		CommentBody:  `<script>alert("xss")</script>`,
	}

	rendered, err := renderEmail(TemplateCommentAdded, payload)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rendered.HTML, "<script>") {
		t.Fatalf("comment body was not HTML-escaped:\n%s", rendered.HTML)
	}
	if !strings.Contains(rendered.HTML, "&lt;script&gt;") {
		t.Fatalf("escaped comment body missing:\n%s", rendered.HTML)
	}
	if strings.Contains(rendered.HTML, "<b>Cannot</b>") {
		t.Fatalf("subject was not HTML-escaped:\n%s", rendered.HTML)
	}

	// The plain-text part keeps the raw comment, which is correct for a
	// text/plain body.
	msg := s.buildMessage("customer@example.com", TemplateCommentAdded, payload)
	if !strings.Contains(msg, "script") {
		t.Fatalf("plain-text body missing the comment:\n%s", msg)
	}
}

func TestSMTPSenderSanitizesHeaderInjection(t *testing.T) {
	s := NewSMTPSender("smtp.example.com", 587, "", "", "no-reply@example.com", "Gregport")

	msg := s.buildMessage("customer@example.com", TemplateTicketCreated, DeliveryPayload{
		OrgName:      "Acme",
		TicketNumber: 1042,
		Subject:      "Hello\r\nBcc: attacker@example.com",
	})

	if strings.Contains(msg, "\r\nBcc:") {
		t.Fatalf("header injection was not prevented:\n%s", msg)
	}
	if !strings.Contains(msg, "Subject: [#1042] Hello Bcc: attacker@example.com") {
		t.Fatalf("subject not sanitized as expected:\n%s", msg)
	}
}

func TestRenderEmailPerTemplate(t *testing.T) {
	payload := DeliveryPayload{
		OrgName:       "Acme",
		TicketNumber:  1042,
		Subject:       "Cannot log in",
		RequesterName: "Ada",
		CommentBody:   "We are on it",
		ReplyTo:       "support@acme.test",
	}

	for _, templateName := range []string{
		TemplateTicketCreated,
		TemplateCommentAdded,
		TemplateTicketAssigned,
		TemplateSLABreached,
	} {
		rendered, err := renderEmail(templateName, payload)
		if err != nil {
			t.Fatalf("%s: %v", templateName, err)
		}
		if rendered.Subject != "[#1042] Cannot log in" {
			t.Fatalf("%s subject: %q", templateName, rendered.Subject)
		}
		if !strings.Contains(rendered.Text, "[#1042] Cannot log in") {
			t.Fatalf("%s text missing ticket number: %q", templateName, rendered.Text)
		}
		if !strings.Contains(rendered.HTML, "[#1042]") {
			t.Fatalf("%s html missing ticket number: %q", templateName, rendered.HTML)
		}
		if !strings.Contains(rendered.Text, "support@acme.test") {
			t.Fatalf("%s text missing reply-to: %q", templateName, rendered.Text)
		}
	}

	agentComment, err := renderEmail(TemplateCommentAdded, payload)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(agentComment.Text, "We are on it") ||
		!strings.Contains(agentComment.HTML, "We are on it") {
		t.Fatalf("comment body missing from render: %+v", agentComment)
	}
}

func TestEmailSubjectFallback(t *testing.T) {
	got := emailSubject(DeliveryPayload{})
	if got != "Ticket update" {
		t.Fatalf("subject=%q", got)
	}

	got = emailSubject(DeliveryPayload{TicketNumber: 7})
	if got != "[#7] Ticket update" {
		t.Fatalf("subject=%q", got)
	}
}

func TestSMTPSenderSendFailsOnBadAddress(t *testing.T) {
	s := NewSMTPSender("127.0.0.1", 9, "", "", "no-reply@example.com", "Gregport")

	err := s.Send(context.Background(), "customer@example.com", TemplateTicketCreated, DeliveryPayload{})
	if err == nil {
		t.Fatal("expected an error connecting to a closed port")
	}
}
