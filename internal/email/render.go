package email

import (
	"bytes"
	"fmt"
	htmltemplate "html/template"
	"strings"
	texttemplate "text/template"
)

// renderedEmail is the pair of bodies sent as a multipart/alternative email.
type renderedEmail struct {
	Subject string
	Text    string
	HTML    string
}

// templateData is the value rendered by the text and HTML templates. The
// Template field selects the body for the delivery's template.
type templateData struct {
	Template string
	DeliveryPayload
}

// textTemplates renders the plain-text part. Keep the copy short and free of
// formatting so it reads well in any client.
var textTemplates = texttemplate.Must(texttemplate.New("email").Parse(`{{- if eq .Template "ticket_created" -}}
Hi {{if .RequesterName}}{{.RequesterName}}{{else}}there{{end}},

Thanks for contacting {{.OrgName}}. We have received your request and will reply as soon as possible.

Your ticket is [#{{.TicketNumber}}] {{.Subject}}.
{{- else if eq .Template "comment_added" -}}
There is a new reply on ticket [#{{.TicketNumber}}] {{.Subject}}.

{{.CommentBody}}
{{- else if eq .Template "ticket_assigned" -}}
Ticket [#{{.TicketNumber}}] {{.Subject}} has been assigned to you.
{{- else if eq .Template "ticket_solved" -}}
Your ticket [#{{.TicketNumber}}] {{.Subject}} has been solved.
{{- if .RatingURL}}
We would love to know how we did. Rate this ticket:
{{.RatingURL}}
{{- end }}
{{- else if eq .Template "sla_breached" -}}
Ticket [#{{.TicketNumber}}] {{.Subject}} has breached its SLA target. Please review it.
{{- else if eq .Template "export_ready" -}}
Your ticket export is ready.

{{.ExportRows}} tickets were exported. Download the CSV here:
{{.ExportURL}}

This link expires in 24 hours.
{{- else -}}
Update for ticket [#{{.TicketNumber}}] {{.Subject}}.
{{- end }}

--
{{.OrgName}}{{if .ReplyTo}}
Reply to: {{.ReplyTo}}{{end}}
`))

// htmlTemplates renders the HTML part. html/template escapes every
// interpolated value, so a customer-supplied ticket subject or comment body
// cannot inject markup.
var htmlTemplates = htmltemplate.Must(htmltemplate.New("email").Parse(`<!doctype html>
<html>
<body style="font-family: -apple-system, Segoe UI, Helvetica, Arial, sans-serif; color: #1f2933; line-height: 1.5;">
{{- if eq .Template "ticket_created" -}}
<p>Hi {{if .RequesterName}}{{.RequesterName}}{{else}}there{{end}},</p>
<p>Thanks for contacting {{.OrgName}}. We have received your request and will reply as soon as possible.</p>
<p>Your ticket is <strong>[#{{.TicketNumber}}] {{.Subject}}</strong>.</p>
{{- else if eq .Template "comment_added" -}}
<p>There is a new reply on ticket <strong>[#{{.TicketNumber}}] {{.Subject}}</strong>.</p>
<blockquote style="margin: 0; padding: 8px 16px; border-left: 3px solid #cbd2d9; white-space: pre-wrap;">{{.CommentBody}}</blockquote>
{{- else if eq .Template "ticket_assigned" -}}
<p>Ticket <strong>[#{{.TicketNumber}}] {{.Subject}}</strong> has been assigned to you.</p>
{{- else if eq .Template "ticket_solved" -}}
<p>Your ticket <strong>[#{{.TicketNumber}}] {{.Subject}}</strong> has been solved.</p>
{{- if .RatingURL}}
<p>We would love to know how we did.</p>
<p><a href="{{.RatingURL}}" style="display: inline-block; padding: 10px 18px; background: #2563eb; color: #ffffff; text-decoration: none; border-radius: 6px;">Rate this ticket</a></p>
{{- end }}
{{- else if eq .Template "sla_breached" -}}
<p>Ticket <strong>[#{{.TicketNumber}}] {{.Subject}}</strong> has breached its SLA target. Please review it.</p>
{{- else if eq .Template "export_ready" -}}
<p>Your ticket export is ready.</p>
<p>{{.ExportRows}} tickets were exported.</p>
<p><a href="{{.ExportURL}}" style="display: inline-block; padding: 10px 18px; background: #2563eb; color: #ffffff; text-decoration: none; border-radius: 6px;">Download CSV</a></p>
<p style="color: #616e7c; font-size: 13px;">This link expires in 24 hours.</p>
{{- else -}}
<p>Update for ticket <strong>[#{{.TicketNumber}}] {{.Subject}}</strong>.</p>
{{- end }}
<hr style="border: 0; border-top: 1px solid #e4e7eb; margin: 24px 0 12px;">
<p style="color: #616e7c; font-size: 13px;">{{.OrgName}}{{if .ReplyTo}}<br>Reply to: {{.ReplyTo}}{{end}}</p>
</body>
</html>
`))

// renderEmail renders both bodies for one delivery. Rendering is pure, so the
// same payload always produces the same email.
func renderEmail(templateName string, payload DeliveryPayload) (renderedEmail, error) {
	// Sanitize interpolated header-derived fields once so the same clean value
	// is used in the body and in the Subject header.
	payload.Subject = sanitizeHeader(payload.Subject)
	data := templateData{Template: templateName, DeliveryPayload: payload}

	var text bytes.Buffer
	if err := textTemplates.Execute(&text, data); err != nil {
		return renderedEmail{}, fmt.Errorf("render text email: %w", err)
	}

	var html bytes.Buffer
	if err := htmlTemplates.Execute(&html, data); err != nil {
		return renderedEmail{}, fmt.Errorf("render html email: %w", err)
	}

	return renderedEmail{
		Subject: emailSubject(payload),
		Text:    text.String(),
		HTML:    html.String(),
	}, nil
}

// emailSubject builds a subject that always carries the ticket number, so
// mail clients and agents can match a thread to a ticket at a glance.
func emailSubject(payload DeliveryPayload) string {
	subject := sanitizeHeader(payload.Subject)
	if subject == "" {
		subject = "Ticket update"
	}
	if payload.TicketNumber > 0 {
		return fmt.Sprintf("[#%d] %s", payload.TicketNumber, subject)
	}
	return subject
}

// sanitizeHeader strips CR/LF from header values to prevent header injection.
func sanitizeHeader(value string) string {
	value = strings.NewReplacer("\r\n", " ", "\r", " ", "\n", " ").Replace(value)
	return strings.TrimSpace(value)
}
