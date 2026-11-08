// Package notifications handles in-app notification fan-out and the
// notification/preference HTTP API.
//
// A notify job is enqueued by the outbox dispatcher for each ticket event that
// should produce notifications. The handler resolves recipients from the
// database (current assignee and ticket watchers) minus the actor, applies
// per-user notification preferences, and inserts notification rows with
// ON CONFLICT DO NOTHING on the (event_id, user_id) dedupe key so a replayed
// job creates no duplicates.
//
// The ticket requester is a customer, not a user, so they never receive an
// in-app notification. Customer updates go through the email delivery path.
package notifications

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Event types that produce in-app notifications. These are also the event
// types a user can configure in their notification preferences.
const (
	EventTicketCreated  = "ticket_created"
	EventTicketAssigned = "ticket_assigned"
	EventNewComment     = "new_comment"
	EventSLABreach      = "sla_breach"
)

// Channels.
const (
	ChannelInApp = "in_app"
	ChannelEmail = "email"
)

// EventTypes is the stable list of configurable notification event types.
func EventTypes() []string {
	return []string{EventTicketCreated, EventTicketAssigned, EventNewComment, EventSLABreach}
}

// Channels is the stable list of notification channels.
func Channels() []string {
	return []string{ChannelInApp, ChannelEmail}
}

// ValidEventType reports whether eventType is a configurable notification type.
func ValidEventType(eventType string) bool {
	for _, t := range EventTypes() {
		if t == eventType {
			return true
		}
	}
	return false
}

// ValidChannel reports whether channel is a known notification channel.
func ValidChannel(channel string) bool {
	for _, c := range Channels() {
		if c == channel {
			return true
		}
	}
	return false
}

// TaskPayload is the JSON body the outbox dispatcher puts on notify tasks. It
// mirrors outbox.EventJob; the actor is used to avoid notifying a user about
// their own action.
type TaskPayload struct {
	EventID   uuid.UUID       `json:"event_id"`
	OrgID     uuid.UUID       `json:"org_id"`
	TicketID  uuid.UUID       `json:"ticket_id"`
	EventType string          `json:"event_type"`
	ActorID   uuid.UUID       `json:"actor_id"`
	Data      json.RawMessage `json:"data"`
}

// Validate rejects payloads that can never produce a notification.
func (p *TaskPayload) Validate() error {
	if p.EventID == uuid.Nil {
		return errors.New("notifications: event_id is required")
	}
	if p.OrgID == uuid.Nil {
		return errors.New("notifications: org_id is required")
	}
	if p.TicketID == uuid.Nil {
		return errors.New("notifications: ticket_id is required")
	}
	if p.EventType == "" {
		return errors.New("notifications: event_type is required")
	}
	return nil
}

// Notification is one persisted notification row.
type Notification struct {
	ID        uuid.UUID
	OrgID     uuid.UUID
	UserID    uuid.UUID
	EventID   uuid.UUID
	Type      string
	TicketID  *uuid.UUID
	CommentID *uuid.UUID
	Payload   json.RawMessage
	ReadAt    *time.Time
	CreatedAt time.Time
}

// NotificationDTO is the API representation of a notification.
type NotificationDTO struct {
	ID        string          `json:"id"                   example:"550e8400-e29b-41d4-a716-446655440000"`
	Type      string          `json:"type"                 example:"new_comment"`
	TicketID  *string         `json:"ticket_id,omitempty"  example:"550e8400-e29b-41d4-a716-446655440000"`
	CommentID *string         `json:"comment_id,omitempty" example:"550e8400-e29b-41d4-a716-446655440000"`
	Payload   json.RawMessage `json:"payload"              swaggertype:"object"`
	ReadAt    *time.Time      `json:"read_at,omitempty"    example:"2026-01-15T10:30:00Z"`
	CreatedAt time.Time       `json:"created_at"           example:"2026-01-15T10:00:00Z"`
}

// NotificationListResponse is the response for GET /v1/notifications.
type NotificationListResponse struct {
	Notifications []NotificationDTO `json:"notifications"`
	NextCursor    string            `json:"next_cursor,omitempty" example:"eyJ0IjoiMjAyNi0wMS0xNVQxMDowMDowMFoiLCJpIjoiNTUwZTg0MDAtIn0"`
}

// Preference is one user notification preference row. The table stores only
// exceptions; a missing row means the default (enabled) applies.
type Preference struct {
	UserID    uuid.UUID
	EventType string
	Channel   string
	Enabled   bool
}

// PreferenceDTO is the API representation of one preference.
type PreferenceDTO struct {
	EventType string `json:"event_type" example:"new_comment"`
	Channel   string `json:"channel"    example:"in_app" enums:"in_app,email"`
	Enabled   bool   `json:"enabled"    example:"true"`
}

// PreferenceListResponse is the response for GET /v1/notification-preferences.
// It always contains the full event-type x channel matrix, with defaults
// filled in for preferences the user has never changed.
type PreferenceListResponse struct {
	Preferences []PreferenceDTO `json:"preferences"`
}

// UpdatePreferencesRequest is the body for PUT /v1/notification-preferences.
// The supplied list replaces the user's stored preferences; omitted entries
// revert to the default (enabled).
type UpdatePreferencesRequest struct {
	Preferences []PreferenceDTO `json:"preferences"`
}

// Validate checks that every preference names a known event type and channel.
func (r UpdatePreferencesRequest) Validate() error {
	if r.Preferences == nil {
		return errors.New("preferences is required")
	}
	seen := make(map[string]struct{}, len(r.Preferences))
	for i, p := range r.Preferences {
		if !ValidEventType(p.EventType) {
			return fmt.Errorf("preferences[%d].event_type: unknown event type %q", i, p.EventType)
		}
		if !ValidChannel(p.Channel) {
			return fmt.Errorf("preferences[%d].channel: unknown channel %q", i, p.Channel)
		}
		key := p.EventType + "\x00" + p.Channel
		if _, ok := seen[key]; ok {
			return fmt.Errorf("preferences[%d]: duplicate %s/%s", i, p.EventType, p.Channel)
		}
		seen[key] = struct{}{}
	}
	return nil
}
