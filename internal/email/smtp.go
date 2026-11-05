package email

import (
	"context"
	"fmt"
	"net"
	"net/smtp"
	"strconv"
	"strings"
)

// SMTPSender delivers email over SMTP using the standard library. It is safe
// for concurrent use: each Send establishes its own connection.
type SMTPSender struct {
	host     string
	port     int
	user     string
	password string
	from     string
	fromName string
}

// NewSMTPSender builds an SMTP sender. Empty user/password means no AUTH.
func NewSMTPSender(host string, port int, user, password, from, fromName string) *SMTPSender {
	return &SMTPSender{
		host:     host,
		port:     port,
		user:     user,
		password: password,
		from:     from,
		fromName: fromName,
	}
}

// Send delivers one email. A nil or empty payload is sent as an empty body.
func (s *SMTPSender) Send(ctx context.Context, to, template string, payload DeliveryPayload) error {
	// ctx is accepted for interface compatibility; net/smtp does not support
	// context cancellation mid-session. Check cancellation before connecting.
	if ctx.Err() != nil {
		return ctx.Err()
	}

	addr := net.JoinHostPort(s.host, strconv.Itoa(s.port))
	auth := smtp.PlainAuth("", s.user, s.password, s.host)

	msg := s.buildMessage(to, template, payload)

	if err := smtp.SendMail(addr, auth, s.from, []string{to}, []byte(msg)); err != nil {
		return fmt.Errorf("smtp send: %w", err)
	}
	return nil
}

func (s *SMTPSender) buildMessage(to, template string, payload DeliveryPayload) string {
	var b strings.Builder

	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=\"utf-8\"\r\n")
	b.WriteString("Content-Transfer-Encoding: 7bit\r\n")

	from := s.from
	if s.fromName != "" {
		from = fmt.Sprintf("%s <%s>", s.fromName, s.from)
	}
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Subject: [" + template + "] " + payload.Subject + "\r\n")
	b.WriteString("\r\n")

	b.WriteString("Event: " + payload.EventType + "\r\n")
	b.WriteString("Org: " + payload.OrgName + "\r\n")
	b.WriteString("Ticket #" + fmt.Sprint(payload.TicketNumber) + ": " + payload.Subject + "\r\n")
	if payload.RequesterName != "" {
		b.WriteString("Requester: " + payload.RequesterName + "\r\n")
	}
	if payload.AssigneeName != "" {
		b.WriteString("Assignee: " + payload.AssigneeName + "\r\n")
	}
	if payload.CommentBody != "" {
		b.WriteString("Comment: " + payload.CommentBody + "\r\n")
	}
	b.WriteString("\r\n")
	b.WriteString(string(payload.EventData))
	b.WriteString("\r\n")

	return b.String()
}
