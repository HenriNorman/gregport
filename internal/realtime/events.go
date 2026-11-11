// Package realtime delivers live ticket updates over Server-Sent Events.
//
// The API is stateless: every instance subscribes to Redis only for the
// organisations that currently have local connections. Redis pub/sub is not
// durable, so a reconnect replays the missing rows from ticket_events before
// switching back to live delivery.
package realtime

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// Event is one message on the org channel and, in JSON form, one SSE data
// payload. Seq is the ticket_events cursor and is also written as the SSE id.
// Ephemeral events such as typing have Seq zero (and no SSE id).
type Event struct {
	Seq          int64           `json:"seq,omitempty"`
	ID           uuid.UUID       `json:"event_id,omitempty"`
	Type         string          `json:"type"`
	TicketID     uuid.UUID       `json:"ticket_id,omitempty"`
	TicketNumber int64           `json:"ticket_number,omitempty"`
	ActorID      *uuid.UUID      `json:"actor_id,omitempty"`
	Data         json.RawMessage `json:"data"`
	CreatedAt    time.Time       `json:"created_at,omitempty"`
}

// Event types that are not backed by a ticket_events row.
const (
	TypeTyping = "typing"
)

// OrgChannel is the Redis pub/sub channel for one organisation.
func OrgChannel(orgID uuid.UUID) string {
	return "org:" + orgID.String() + ":events"
}

// safeEventFields is an allow-list for the data object. It is deliberately
// explicit: an internal field added to a ticket event later must not leak to
// agents just because it was written to the outbox.
var safeEventFields = map[string][]string{
	"ticket.created":              {"number", "subject", "priority"},
	"ticket.status_changed":       {"old", "new"},
	"ticket.assigned":             {"old_assignee_id", "new_assignee_id", "old_team_id", "new_team_id", "auto"},
	"comment.added":               {"comment_id", "is_internal"},
	"ticket.tagged":               {"tag"},
	"ticket.untagged":             {"tag"},
	"ticket.merged":               {"into_number", "into_id"},
	"ticket.merge_target_updated": {"from_number", "from_id"},
	"ticket.closed":               {"number", "resolved_at"},
	"ticket.deleted":              {},
	"sla.breached":                {"number", "breach_types", "first_response_due_at", "resolution_due_at"},
}

// SafeData filters an event's data object down to the fields agents may see.
// Unknown event types yield an empty object rather than passing everything
// through.
func SafeData(eventType string, raw []byte) json.RawMessage {
	allowed, ok := safeEventFields[eventType]
	if !ok || len(raw) == 0 {
		return json.RawMessage(`{}`)
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return json.RawMessage(`{}`)
	}

	out := make(map[string]json.RawMessage, len(allowed))
	for _, name := range allowed {
		if value, present := fields[name]; present {
			out[name] = value
		}
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return encoded
}
