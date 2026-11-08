// Package email plans and delivers transactional email for ticket events.
// Planning is idempotent: the unique (event_id, to_email, template) index
// means processing the same event twice reuses one delivery row instead of
// creating a second email.
package email

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Templates identify the rendered email. They are part of the delivery
// idempotency key.
const (
	TemplateTicketCreated  = "ticket_created"
	TemplateCommentAdded   = "comment_added"
	TemplateTicketAssigned = "ticket_assigned"
	TemplateSLABreached    = "sla_breached"
	TemplateTicketSolved   = "ticket_solved"
)

// Errors returned by the service. Missing tickets and comments are permanent
// conditions: the event points at a row that no longer exists, so a retry
// cannot help.
var (
	ErrTicketNotFound  = errors.New("email: ticket not found")
	ErrCommentNotFound = errors.New("email: comment not found")
)

// TaskPayload is the JSON body the outbox dispatcher puts on email tasks.
// It mirrors outbox.EventJob without importing the dispatcher.
type TaskPayload struct {
	EventID   uuid.UUID       `json:"event_id"`
	OrgID     uuid.UUID       `json:"org_id"`
	TicketID  uuid.UUID       `json:"ticket_id"`
	EventType string          `json:"event_type"`
	Data      json.RawMessage `json:"data"`
}

// Validate rejects payloads that can never produce a delivery.
func (p *TaskPayload) Validate() error {
	if p.EventID == uuid.Nil {
		return errors.New("email: event_id is required")
	}
	if p.OrgID == uuid.Nil {
		return errors.New("email: org_id is required")
	}
	if p.TicketID == uuid.Nil {
		return errors.New("email: ticket_id is required")
	}
	if p.EventType == "" {
		return errors.New("email: event_type is required")
	}
	if len(p.Data) == 0 || string(p.Data) == "null" {
		p.Data = json.RawMessage(`{}`)
	}
	return nil
}

// TicketContext is the org-scoped ticket data templates and recipient rules
// need. Emails are always resolved from the database, never trusted from the
// task payload.
type TicketContext struct {
	OrgID          uuid.UUID
	TicketID       uuid.UUID
	Number         int64
	Subject        string
	RequesterID    uuid.UUID
	RequesterEmail string
	RequesterName  string
	AssigneeEmail  string
	AssigneeName   string
	OrgName        string
	SupportEmail   string
}

// CommentContext is the comment an event refers to.
type CommentContext struct {
	AuthorType string
	Body       string
	IsInternal bool
}

// DeliveryPayload is the data a template renders. It intentionally contains
// no secrets, only ticket metadata already visible to the recipient.
type DeliveryPayload struct {
	EventType     string `json:"event_type"`
	OrgName       string `json:"org_name"`
	TicketNumber  int64  `json:"ticket_number"`
	Subject       string `json:"subject"`
	RequesterName string `json:"requester_name,omitempty"`
	AssigneeName  string `json:"assignee_name,omitempty"`
	CommentBody   string `json:"comment_body,omitempty"`
	// ReplyTo is set on customer-facing emails so a customer reply goes to the
	// organization's support inbox rather than the shared From address.
	ReplyTo string `json:"reply_to,omitempty"`
	// RatingURL is the signed, expiring satisfaction-rating link included in
	// the solved-ticket email.
	RatingURL string          `json:"rating_url,omitempty"`
	EventData json.RawMessage `json:"event_data,omitempty"`
}

// PlannedDelivery is one recipient/template pair before it is persisted.
type PlannedDelivery struct {
	ToEmail  string
	Template string
	Payload  DeliveryPayload
}

// Delivery is a persisted email_deliveries row.
type Delivery struct {
	ID        uuid.UUID
	OrgID     uuid.UUID
	EventID   uuid.UUID
	ToEmail   string
	Template  string
	Payload   json.RawMessage
	Status    string
	Attempts  int
	CreatedAt time.Time
}

// String makes log output readable.
func (d Delivery) String() string {
	return fmt.Sprintf("%s -> %s (%s)", d.Template, d.ToEmail, d.Status)
}

// Delivery statuses match the delivery_status enum.
const (
	StatusPending = "pending"
	StatusSent    = "sent"
	StatusFailed  = "failed"
	StatusDead    = "dead"
)
