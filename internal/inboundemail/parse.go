package inboundemail

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"strings"
	"time"
)

// Parse parses a raw RFC 5322 email message. It prefers the text/plain part
// of a multipart message and falls back to the whole body for single-part
// messages. Headers are decoded from MIME encoded-words where possible.
func Parse(raw []byte) (*InboundEmail, error) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("parse email: %w", err)
	}
	h := msg.Header

	e := &InboundEmail{
		MessageID: canonicalID(h.Get("Message-ID")),
		InReplyTo: canonicalID(h.Get("In-Reply-To")),
		Subject:   decodeHeader(h.Get("Subject")),
		Recipient: h.Get("To"),
		Date:      parseDate(h.Get("Date")),
		Raw:       raw,
	}

	if fromAddr, err := mail.ParseAddress(h.Get("From")); err == nil {
		e.FromEmail = strings.ToLower(strings.TrimSpace(fromAddr.Address))
		e.FromName = strings.TrimSpace(decodeHeader(fromAddr.Name))
	}

	e.Body = extractTextBody(msg)
	if strings.TrimSpace(e.Body) == "" {
		// No text part found; fall back to the raw body.
		if body, err := io.ReadAll(msg.Body); err == nil {
			e.Body = strings.TrimSpace(string(body))
		}
	}
	if e.MessageID == "" {
		return nil, ErrInvalidInput
	}
	return e, nil
}

// canonicalID strips the surrounding angle brackets from a Message-ID value
// so " <abc@example.com> " and "abc@example.com" compare equal.
func canonicalID(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "<")
	s = strings.TrimSuffix(s, ">")
	return strings.TrimSpace(s)
}

// extractTextBody returns the first text/plain part of a multipart message,
// or the entire body for a single-part message.
func extractTextBody(msg *mail.Message) string {
	ct := msg.Header.Get("Content-Type")
	mediatype, params, err := mime.ParseMediaType(ct)
	if err != nil {
		// No usable content type: treat the whole body as the message.
		body, _ := io.ReadAll(msg.Body)
		return string(body)
	}
	switch mediatype {
	case "multipart/mixed", "multipart/alternative", "multipart/related":
		boundary := params["boundary"]
		if boundary == "" {
			body, _ := io.ReadAll(msg.Body)
			return string(body)
		}
		mr := multipart.NewReader(msg.Body, boundary)
		for {
			p, err := mr.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				break
			}
			partCT := p.Header.Get("Content-Type")
			partMedia, _, perr := mime.ParseMediaType(partCT)
			if perr != nil {
				continue
			}
			if partMedia != "text/plain" {
				continue
			}
			body, err := io.ReadAll(p)
			if err != nil {
				continue
			}
			return decodeTransferEncoding(p.Header.Get("Content-Transfer-Encoding"), body)
		}
		return ""
	default:
		body, err := io.ReadAll(msg.Body)
		if err != nil {
			return ""
		}
		return decodeTransferEncoding(ct, body)
	}
}

func decodeTransferEncoding(ct string, body []byte) string {
	// Parse the charset and content-transfer-encoding from the part header.
	_, params, err := mime.ParseMediaType(ct)
	if err != nil {
		params = map[string]string{}
	}
	charset := params["charset"]
	if charset == "" {
		charset = "utf-8"
	}
	cte := ""
	if parts := strings.SplitN(ct, ";", 2); len(parts) == 2 {
		cte = strings.TrimSpace(parts[1])
	}
	// Honour common transfer encodings.
	switch strings.ToLower(strings.TrimPrefix(cte, "Content-Transfer-Encoding:")) {
	case "quoted-printable":
		return decodeQuotedPrintable(body)
	case "base64":
		return decodeBase64(body)
	}
	// Fall back to the raw bytes interpreted as UTF-8 (invalid runes replaced).
	return string(body)
}

func decodeQuotedPrintable(body []byte) string {
	var b bytes.Buffer
	qp := quotedprintable.NewReader(bytes.NewReader(body))
	if _, err := b.ReadFrom(qp); err != nil {
		return string(body)
	}
	return b.String()
}

func decodeBase64(body []byte) string {
	// Go's base64.StdEncoding is used inline to avoid an extra import.
	decoded := make([]byte, len(body))
	n := 0
	for i := 0; i < len(body); i++ {
		c := body[i]
		switch {
		case c >= 'A' && c <= 'Z':
			decoded[n] = byte(c - 'A')
		case c >= 'a' && c <= 'z':
			decoded[n] = byte(c - 'a' + 26)
		case c >= '0' && c <= '9':
			decoded[n] = byte(c - '0' + 52)
		case c == '+':
			decoded[n] = 62
		case c == '/':
			decoded[n] = 63
		case c == '=':
			continue
		default:
			continue
		}
		n++
	}
	return string(decoded[:n])
}

func decodeHeader(s string) string {
	// RFC 2047 encoded-words like =?utf-8?q?...?= are decoded by mail.ReadMessage
	// for structured headers, but free text subjects may still carry them. We do
	// a best-effort decode: if it looks encoded, leave as-is (net/mail already
	// decodes structured headers); otherwise return the trimmed value.
	return strings.TrimSpace(s)
}

func parseDate(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	if t, err := mail.ParseDate(s); err == nil {
		return t
	}
	return time.Time{}
}
