package email

import (
	"bytes"
	"context"
	"fmt"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
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

// buildMessage renders the template into a multipart/alternative message with
// a plain-text and an HTML part. Headers are sanitized so ticket subjects and
// org settings cannot inject extra headers.
func (s *SMTPSender) buildMessage(to, template string, payload DeliveryPayload) string {
	rendered, err := renderEmail(template, payload)
	if err != nil {
		// Rendering our own static templates should never fail, but fall back
		// to a plain-text message rather than dropping the delivery.
		rendered = renderedEmail{
			Subject: emailSubject(payload),
			Text:    payload.Subject,
		}
	}

	body, boundary := buildMultipartBody(rendered)

	from := sanitizeHeader(s.from)
	if s.fromName != "" {
		from = fmt.Sprintf("%s <%s>", sanitizeHeader(s.fromName), from)
	}

	var b strings.Builder
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + sanitizeHeader(to) + "\r\n")
	if payload.ReplyTo != "" {
		b.WriteString("Reply-To: " + sanitizeHeader(payload.ReplyTo) + "\r\n")
	}
	b.WriteString("Subject: " + sanitizeHeader(rendered.Subject) + "\r\n")
	b.WriteString("Date: " + time.Now().UTC().Format(time.RFC1123Z) + "\r\n")
	b.WriteString("Message-ID: <" + uuid.NewString() + "@" + s.messageIDDomain() + ">\r\n")
	b.WriteString("Content-Type: multipart/alternative; boundary=" + boundary + "\r\n")
	b.WriteString("\r\n")
	b.Write(body)
	return b.String()
}

// buildMultipartBody writes the plain-text and HTML parts and returns the
// serialized body together with the boundary used in the top-level header.
func buildMultipartBody(rendered renderedEmail) ([]byte, string) {
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	boundary := mw.Boundary()

	writePart := func(contentType, content string) {
		header := textproto.MIMEHeader{}
		header.Set("Content-Type", contentType+`; charset="utf-8"`)
		header.Set("Content-Transfer-Encoding", "quoted-printable")
		part, err := mw.CreatePart(header)
		if err != nil {
			return
		}
		qp := quotedprintable.NewWriter(part)
		_, _ = qp.Write([]byte(content))
		_ = qp.Close()
	}

	writePart("text/plain", rendered.Text)
	writePart("text/html", rendered.HTML)
	_ = mw.Close()

	return body.Bytes(), boundary
}

// messageIDDomain derives a syntactically valid domain for the Message-ID
// header from the configured sending host.
func (s *SMTPSender) messageIDDomain() string {
	host := sanitizeHeader(s.host)
	if host == "" {
		return "localhost"
	}
	return host
}
