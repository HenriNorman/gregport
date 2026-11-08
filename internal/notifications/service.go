// Package notifications applies notification preferences and fans
// notifications out to the right users for each ticket event.
package notifications

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
)

// Service computes recipients, applies preferences and exposes the
// notification/preference operations used by the HTTP API.
type Service struct {
	repo Repo
}

// NewService builds a notification service.
func NewService(repo Repo) *Service {
	return &Service{repo: repo}
}

// FanOut computes recipients for an event, applies preferences, and inserts
// one notification per recipient. Returns the number of notifications actually
// inserted (dedupe means replays insert zero).
func (s *Service) FanOut(
	ctx context.Context,
	orgID uuid.UUID,
	eventID uuid.UUID,
	eventType string,
	ticketCtx TicketContext,
	commentCtx *CommentContext,
	watchers []uuid.UUID,
	actorID uuid.UUID,
) (int, error) {
	recipients := computeRecipients(ticketCtx, watchers, actorID)

	inserted := 0
	for _, userID := range recipients {
		enabled, err := s.isEnabled(ctx, userID, eventType)
		if err != nil {
			return inserted, fmt.Errorf("check preferences for user %s: %w", userID, err)
		}
		if !enabled {
			continue
		}
		payload := buildPayload(eventType, ticketCtx, commentCtx)
		_, ins, err := s.repo.InsertNotification(ctx, Notification{
			OrgID:     orgID,
			UserID:    userID,
			EventID:   eventID,
			Type:      eventType,
			TicketID:  &ticketCtx.TicketID,
			CommentID: commentCtxID(commentCtx),
			Payload:   payload,
		})
		if err != nil {
			return inserted, fmt.Errorf("insert notification for user %s: %w", userID, err)
		}
		if ins {
			inserted++
		}
	}
	return inserted, nil
}

// computeRecipients returns the set of user IDs that should be notified for an
// event: the current assignee and ticket watchers, minus the actor.
//
// The ticket requester is a customer, not a user, so they are intentionally
// excluded; customers receive ticket updates through email.
func computeRecipients(tc TicketContext, watchers []uuid.UUID, actorID uuid.UUID) []uuid.UUID {
	recipients := make(map[uuid.UUID]struct{})

	if tc.AssigneeID != nil {
		recipients[*tc.AssigneeID] = struct{}{}
	}
	for _, id := range watchers {
		recipients[id] = struct{}{}
	}

	// The actor never receives a notification for their own action.
	delete(recipients, actorID)

	out := make([]uuid.UUID, 0, len(recipients))
	for id := range recipients {
		out = append(out, id)
	}
	return out
}

// isEnabled checks whether a user has in-app notifications enabled for an
// event type. Missing preference rows default to enabled.
func (s *Service) isEnabled(ctx context.Context, userID uuid.UUID, eventType string) (bool, error) {
	prefs, err := s.repo.ListPreferences(ctx, userID)
	if err != nil {
		return false, err
	}
	for _, p := range prefs {
		if p.EventType == eventType && p.Channel == ChannelInApp {
			return p.Enabled, nil
		}
	}
	return true, nil
}

// buildPayload creates the stable JSON payload for a notification.
func buildPayload(eventType string, tc TicketContext, commentCtx *CommentContext) json.RawMessage {
	payload := map[string]any{
		"ticket_number":  tc.Number,
		"ticket_subject": tc.Subject,
		"event_type":     eventType,
	}
	if commentCtx != nil {
		payload["comment_body"] = commentCtx.Body
		payload["comment_author_type"] = commentCtx.AuthorType
	}
	raw, _ := json.Marshal(payload)
	return raw
}

// commentCtxID returns the comment ID from a comment context, or nil.
func commentCtxID(c *CommentContext) *uuid.UUID {
	if c == nil {
		return nil
	}
	return &c.ID
}

// List returns the caller's notifications in one org, newest first.
func (s *Service) List(ctx context.Context, orgID, userID uuid.UUID, unreadOnly bool, limit int, cursor string) ([]NotificationDTO, string, error) {
	ns, next, err := s.repo.ListNotifications(ctx, orgID, userID, unreadOnly, limit, cursor)
	if err != nil {
		return nil, "", err
	}
	out := make([]NotificationDTO, 0, len(ns))
	for _, n := range ns {
		out = append(out, toNotificationDTO(n))
	}
	return out, next, nil
}

// MarkRead marks one notification as read for the caller.
func (s *Service) MarkRead(ctx context.Context, orgID, userID, notificationID uuid.UUID) error {
	return s.repo.MarkRead(ctx, orgID, userID, notificationID)
}

// MarkAllRead marks every unread notification for the caller in one org.
func (s *Service) MarkAllRead(ctx context.Context, orgID, userID uuid.UUID) (int64, error) {
	return s.repo.MarkAllRead(ctx, orgID, userID)
}

// Preferences returns the full event-type x channel matrix for a user, filling
// defaults for anything the user has never changed.
func (s *Service) Preferences(ctx context.Context, userID uuid.UUID) ([]PreferenceDTO, error) {
	stored, err := s.repo.ListPreferences(ctx, userID)
	if err != nil {
		return nil, err
	}
	return preferenceMatrix(stored), nil
}

// UpdatePreferences replaces a user's stored preference exceptions and
// returns the resulting full matrix.
func (s *Service) UpdatePreferences(ctx context.Context, userID uuid.UUID, req UpdatePreferencesRequest) ([]PreferenceDTO, error) {
	if err := req.Validate(); err != nil {
		return nil, err
	}
	prefs := make([]Preference, 0, len(req.Preferences))
	for _, p := range req.Preferences {
		prefs = append(prefs, Preference{
			UserID:    userID,
			EventType: p.EventType,
			Channel:   p.Channel,
			Enabled:   p.Enabled,
		})
	}
	if err := s.repo.ReplacePreferences(ctx, userID, prefs); err != nil {
		return nil, err
	}
	return s.Preferences(ctx, userID)
}

// preferenceMatrix fills the full default-on matrix in a deterministic order.
func preferenceMatrix(stored []Preference) []PreferenceDTO {
	type key struct {
		eventType string
		channel   string
	}
	overrides := make(map[key]bool, len(stored))
	for _, p := range stored {
		overrides[key{p.EventType, p.Channel}] = p.Enabled
	}

	out := make([]PreferenceDTO, 0, len(EventTypes())*len(Channels()))
	for _, eventType := range EventTypes() {
		for _, channel := range Channels() {
			enabled := true
			if v, ok := overrides[key{eventType, channel}]; ok {
				enabled = v
			}
			out = append(out, PreferenceDTO{
				EventType: eventType,
				Channel:   channel,
				Enabled:   enabled,
			})
		}
	}
	return out
}

func toNotificationDTO(n Notification) NotificationDTO {
	var ticketID, commentID *string
	if n.TicketID != nil {
		s := n.TicketID.String()
		ticketID = &s
	}
	if n.CommentID != nil {
		s := n.CommentID.String()
		commentID = &s
	}
	return NotificationDTO{
		ID:        n.ID.String(),
		Type:      n.Type,
		TicketID:  ticketID,
		CommentID: commentID,
		Payload:   n.Payload,
		ReadAt:    n.ReadAt,
		CreatedAt: n.CreatedAt,
	}
}
