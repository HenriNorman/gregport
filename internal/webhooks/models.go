// Package webhooks exposes tenant-scoped webhook subscriptions, signs and
// delivers event payloads, records a delivery log and supports redelivery.
//
// Delivery is at-least-once. A unique (subscription_id, event_id) index makes
// planning idempotent under outbox replay, and every attempt is recorded with
// its HTTP status, a truncated response body and the retry schedule state.
package webhooks

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/platform/httpx"
)

// Supported event types. They match the outbox event names; only events in
// this list can be subscribed to.
const (
	EventTicketCreated       = "ticket.created"
	EventTicketAssigned      = "ticket.assigned"
	EventTicketStatusChanged = "ticket.status_changed"
	EventTicketClosed        = "ticket.closed"
	EventTicketDeleted       = "ticket.deleted"
	EventTicketMerged        = "ticket.merged"
	EventTicketTagged        = "ticket.tagged"
	EventTicketUntagged      = "ticket.untagged"
	EventCommentAdded        = "comment.added"
	EventSLABreached         = "sla.breached"
	// EventPing is used by the test endpoint and is never subscribable.
	EventPing = "webhook.ping"
)

// SupportedEventTypes returns the event names a subscription may request.
func SupportedEventTypes() []string {
	return []string{
		EventTicketCreated,
		EventTicketAssigned,
		EventTicketStatusChanged,
		EventTicketClosed,
		EventTicketDeleted,
		EventTicketMerged,
		EventTicketTagged,
		EventTicketUntagged,
		EventCommentAdded,
		EventSLABreached,
	}
}

// ValidEventType reports whether eventType can be subscribed to.
func ValidEventType(eventType string) bool {
	for _, supported := range SupportedEventTypes() {
		if eventType == supported {
			return true
		}
	}
	return false
}

// Delivery statuses match the delivery_status enum.
const (
	StatusPending = "pending"
	StatusSent    = "sent"
	StatusFailed  = "failed"
	StatusDead    = "dead"
)

// MaxResponseBodyBytes is how much of a response body is kept in the delivery
// log. A receiver can return an arbitrarily large body; the log only needs
// enough to debug a failure.
const MaxResponseBodyBytes = 4096

// MaxEventsPerSubscription bounds the events array so one request cannot
// create an unbounded jsonb document.
const MaxEventsPerSubscription = 32

// ErrNotFound is returned for a missing or foreign subscription/delivery.
var ErrNotFound = errors.New("webhooks: not found")

// ErrDeliveryNotFound is returned when a delivery does not belong to the
// given subscription and org. It maps to HTTP 404.
var ErrDeliveryNotFound = httpx.NotFound("webhook_delivery_not_found", "webhook delivery not found")

// API sentinels. They are *httpx.APIError values so WriteError maps them.
var (
	ErrInvalidURL      = httpx.Unprocessable("invalid_url", "webhook url is invalid or resolves to a private address")
	ErrInvalidEvents   = httpx.Unprocessable("invalid_events", "events must be a non-empty list of supported event types")
	ErrWebhookNotFound = httpx.NotFound("webhook_not_found", "webhook not found")
	ErrInvalidWebhook  = httpx.Unprocessable("invalid_webhook", "webhook is invalid")
	ErrDeliveryGone    = httpx.NotFound("webhook_delivery_not_found", "webhook delivery not found")
	ErrInactive        = httpx.Conflict("webhook_inactive", "webhook is disabled; re-enable it before testing")
)

// Subscription is one webhook_subscriptions row. Secret is returned only from
// the create call; list/get DTOs never expose it.
type Subscription struct {
	ID                  uuid.UUID
	OrgID               uuid.UUID
	URL                 string
	Secret              string
	Events              []string
	IsActive            bool
	ConsecutiveFailures int
	LastFailureAt       *time.Time
	DisabledAt          *time.Time
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// CreateSubscriptionRequest is the payload for POST /v1/webhooks.
type CreateSubscriptionRequest struct {
	URL    string   `json:"url"    example:"https://example.com/gregport/hooks"`
	Events []string `json:"events" example:"ticket.created,comment.added"`
}

// UpdateSubscriptionRequest is a partial update. Omitted fields keep their
// value. Setting is_active=true re-enables a subscription disabled after
// repeated delivery failures and resets its failure counter.
type UpdateSubscriptionRequest struct {
	URL      *string   `json:"url,omitempty"       example:"https://example.com/gregport/hooks"`
	Events   *[]string `json:"events,omitempty"    example:"ticket.created"`
	IsActive *bool     `json:"is_active,omitempty" example:"true"`
}

// SubscriptionDTO is the API representation of a subscription. Secret is only
// populated on create, because it is the HMAC key receivers need.
type SubscriptionDTO struct {
	ID                  uuid.UUID  `json:"id"                             example:"550e8400-e29b-41d4-a716-446655440000"`
	URL                 string     `json:"url"                            example:"https://example.com/gregport/hooks"`
	Events              []string   `json:"events"                         example:"ticket.created"`
	IsActive            bool       `json:"is_active"                      example:"true"`
	ConsecutiveFailures int        `json:"consecutive_failures"           example:"0"`
	DisabledAt          *time.Time `json:"disabled_at,omitempty"          example:"2026-01-15T10:30:00Z"`
	Secret              string     `json:"secret,omitempty"               example:"whsec_..." swaggertype:"string"`
	CreatedAt           time.Time  `json:"created_at"                     example:"2026-01-15T10:00:00Z"`
	UpdatedAt           time.Time  `json:"updated_at"                     example:"2026-01-15T10:00:00Z"`
}

// SubscriptionListResponse is the response for GET /v1/webhooks.
type SubscriptionListResponse struct {
	Webhooks []SubscriptionDTO `json:"webhooks"`
}

// Delivery is one webhook_deliveries row.
type Delivery struct {
	ID             uuid.UUID
	OrgID          uuid.UUID
	SubscriptionID uuid.UUID
	EventID        *uuid.UUID
	EventType      string
	Payload        json.RawMessage
	Status         string
	Attempts       int
	ResponseStatus *int
	ResponseBody   *string
	LastError      *string
	NextRetryAt    *time.Time
	DeliveredAt    *time.Time
	DurationMS     *int
	CreatedAt      time.Time
}

// DeliveryDTO is the API representation of a delivery-log row.
type DeliveryDTO struct {
	ID             uuid.UUID  `json:"id"                        example:"550e8400-e29b-41d4-a716-446655440000"`
	EventID        *uuid.UUID `json:"event_id,omitempty"        example:"550e8400-e29b-41d4-a716-446655440000"`
	EventType      string     `json:"event_type"                example:"ticket.created"`
	Status         string     `json:"status"                    example:"failed" enums:"pending,sent,failed,dead"`
	Attempts       int        `json:"attempts"                  example:"3"`
	ResponseStatus *int       `json:"response_status,omitempty" example:"500"`
	ResponseBody   string     `json:"response_body,omitempty"   example:"internal error"`
	LastError      string     `json:"last_error,omitempty"      example:"HTTP 500"`
	NextRetryAt    *time.Time `json:"next_retry_at,omitempty"   example:"2026-01-15T10:31:00Z"`
	DeliveredAt    *time.Time `json:"delivered_at,omitempty"    example:"2026-01-15T10:30:00Z"`
	DurationMS     *int       `json:"duration_ms,omitempty"     example:"184"`
	CreatedAt      time.Time  `json:"created_at"                example:"2026-01-15T10:00:00Z"`
}

// DeliveryListResponse is the response for GET /v1/webhooks/{id}/deliveries.
type DeliveryListResponse struct {
	Deliveries []DeliveryDTO `json:"deliveries"`
	NextCursor string        `json:"next_cursor,omitempty" example:"eyJ0IjoiMjAyNi0wMS0xNVQxMDowMDowMFoiLCJpIjoiNTUwZTg0MDAtIn0"`
}

// TicketSummary is the ticket snapshot embedded in a webhook envelope. It is
// resolved from the database at delivery-planning time; callers never supply
// it.
type TicketSummary struct {
	ID          uuid.UUID  `json:"id"`
	Number      int64      `json:"number"`
	Subject     string     `json:"subject"`
	Status      string     `json:"status"`
	Priority    string     `json:"priority"`
	RequesterID uuid.UUID  `json:"requester_id"`
	AssigneeID  *uuid.UUID `json:"assignee_id,omitempty"`
}

// EnvelopeData is the `data` member of the webhook envelope. It carries a
// ticket snapshot plus the raw outbox event data so a receiver gets both
// context and the transition that fired.
type EnvelopeData struct {
	Ticket *TicketSummary  `json:"ticket,omitempty"`
	Event  json.RawMessage `json:"event,omitempty"`
}

// Envelope is the JSON body delivered to a subscription.
type Envelope struct {
	ID        uuid.UUID    `json:"id"`
	Type      string       `json:"type"`
	CreatedAt time.Time    `json:"created_at"`
	Data      EnvelopeData `json:"data"`
}

// OutboxEvent is the subset of an outbox.EventJob the dispatcher needs. It
// mirrors the async payload without importing the outbox package.
type OutboxEvent struct {
	EventID   uuid.UUID       `json:"event_id"`
	OrgID     uuid.UUID       `json:"org_id"`
	TicketID  uuid.UUID       `json:"ticket_id"`
	EventType string          `json:"event_type"`
	Data      json.RawMessage `json:"data"`
}

// Validate rejects events that can never produce a delivery.
func (e *OutboxEvent) Validate() error {
	if e.EventID == uuid.Nil {
		return errors.New("webhooks: event_id is required")
	}
	if e.OrgID == uuid.Nil {
		return errors.New("webhooks: org_id is required")
	}
	if e.TicketID == uuid.Nil {
		return errors.New("webhooks: ticket_id is required")
	}
	if e.EventType == "" {
		return errors.New("webhooks: event_type is required")
	}
	if len(e.Data) == 0 || string(e.Data) == "null" {
		e.Data = json.RawMessage(`{}`)
	}
	return nil
}

// DeliveryTaskPayload is the queue payload for one delivery attempt.
type DeliveryTaskPayload struct {
	DeliveryID uuid.UUID `json:"delivery_id"`
}

// String makes delivery logs readable.
func (d Delivery) String() string {
	return fmt.Sprintf("%s %s (%s)", d.EventType, d.ID, d.Status)
}
