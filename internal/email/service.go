package email

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/ratings"
)

// Event types this consumer handles. They match the outbox consumer map.
const (
	EventTicketCreated       = "ticket.created"
	EventCommentAdded        = "comment.added"
	EventTicketAssigned      = "ticket.assigned"
	EventSLABreached         = "sla.breached"
	EventTicketStatusChanged = "ticket.status_changed"
)

// Repo is the data access the email service needs: loading ticket/comment
// context and persisting delivery rows. *Repository satisfies it; tests can
// substitute a fake.
type Repo interface {
	LoadTicketContext(ctx context.Context, orgID, ticketID uuid.UUID) (TicketContext, error)
	LoadComment(ctx context.Context, orgID, commentID uuid.UUID) (CommentContext, error)
	InsertDelivery(ctx context.Context, orgID, eventID uuid.UUID, planned PlannedDelivery) (Delivery, error)
	MarkSent(ctx context.Context, id uuid.UUID) error
	RecordAttempt(ctx context.Context, id uuid.UUID, status string, attempts int, nextRetryAt *time.Time, lastError *string) error
}

// Service turns ticket events into pending email deliveries.
type Service struct {
	repo          Repo
	ratingLinks   *ratings.TokenManager
	publicBaseURL string
}

func NewService(repo Repo) *Service {
	return &Service{repo: repo}
}

// SetRatingLinks configures the signer and customer-facing base URL used for
// the satisfaction link in solved-ticket email. Without it, the solved email
// is still planned but carries no link.
func (s *Service) SetRatingLinks(tokens *ratings.TokenManager, publicBaseURL string) {
	s.ratingLinks = tokens
	s.publicBaseURL = publicBaseURL
}

// Plan resolves the recipients for an event and persists one pending delivery
// per recipient/template pair. It is safe to call repeatedly: the unique
// index returns the existing row instead of creating a duplicate.
//
// Missing tickets or comments are reported with their sentinel error so the
// handler can log them and stop retrying.
func (s *Service) Plan(ctx context.Context, p TaskPayload) ([]Delivery, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if !supportedEvent(p.EventType) {
		return nil, nil
	}

	tc, err := s.repo.LoadTicketContext(ctx, p.OrgID, p.TicketID)
	if err != nil {
		return nil, err
	}

	var comment *CommentContext
	if p.EventType == EventCommentAdded {
		commentID, err := commentIDFromData(p.Data)
		if err != nil {
			return nil, err
		}
		if commentID == uuid.Nil {
			// The event has no comment reference; nothing to notify about.
			return nil, nil
		}
		c, err := s.repo.LoadComment(ctx, p.OrgID, commentID)
		if err != nil {
			if errors.Is(err, ErrCommentNotFound) {
				return nil, ErrCommentNotFound
			}
			return nil, err
		}
		comment = &c
	}

	ratingURL := ""
	if p.EventType == EventTicketStatusChanged && isSolvedEvent(p.Data) && s.ratingLinks != nil {
		link, err := s.ratingLinks.Link(s.publicBaseURL, tc.TicketID, tc.RequesterID)
		if err != nil {
			return nil, fmt.Errorf("sign rating link: %w", err)
		}
		ratingURL = link
	}

	planned := planDeliveries(tc, comment, p, ratingURL)
	if len(planned) == 0 {
		return nil, nil
	}

	out := make([]Delivery, 0, len(planned))
	for _, item := range planned {
		d, err := s.repo.InsertDelivery(ctx, p.OrgID, p.EventID, item)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, nil
}

func supportedEvent(eventType string) bool {
	switch eventType {
	case EventTicketCreated, EventCommentAdded, EventTicketAssigned, EventSLABreached, EventTicketStatusChanged:
		return true
	default:
		return false
	}
}

// isSolvedEvent reports whether a status-change event moved the ticket to
// solved. Other transitions produce no customer email.
func isSolvedEvent(data json.RawMessage) bool {
	var body struct {
		New string `json:"new"`
	}
	if len(data) == 0 {
		return false
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return false
	}
	return body.New == "solved"
}

func commentIDFromData(data json.RawMessage) (uuid.UUID, error) {
	var body struct {
		CommentID uuid.UUID `json:"comment_id"`
	}
	if len(data) == 0 {
		return uuid.Nil, nil
	}
	if err := json.Unmarshal(data, &body); err != nil {
		return uuid.Nil, fmt.Errorf("decode event data: %w", err)
	}
	return body.CommentID, nil
}

// planDeliveries is pure: the same ticket context and comment always produce
// the same recipients. Keeping it separate makes the notification rules
// table-testable without a database.
func planDeliveries(tc TicketContext, comment *CommentContext, p TaskPayload, ratingURL string) []PlannedDelivery {
	base := DeliveryPayload{
		EventType:     p.EventType,
		OrgName:       tc.OrgName,
		TicketNumber:  tc.Number,
		Subject:       tc.Subject,
		RequesterName: tc.RequesterName,
		AssigneeName:  tc.AssigneeName,
		RatingURL:     ratingURL,
		EventData:     p.Data,
	}

	switch p.EventType {
	case EventTicketCreated:
		base.ReplyTo = tc.SupportEmail
		return one(tc.RequesterEmail, TemplateTicketCreated, base)

	case EventCommentAdded:
		if comment == nil || comment.IsInternal {
			return nil
		}
		base.CommentBody = comment.Body
		switch comment.AuthorType {
		case "customer":
			// A customer reply goes to the agent handling the ticket, or to
			// the shared support inbox if nobody is assigned.
			if tc.AssigneeEmail != "" {
				return one(tc.AssigneeEmail, TemplateCommentAdded, base)
			}
			return one(tc.SupportEmail, TemplateCommentAdded, base)
		case "user":
			// Only agent-authored public comments go to the customer. System
			// notes and internal comments never produce a customer email.
			base.ReplyTo = tc.SupportEmail
			return one(tc.RequesterEmail, TemplateCommentAdded, base)
		default:
			return nil
		}

	case EventTicketAssigned:
		return one(tc.AssigneeEmail, TemplateTicketAssigned, base)

	case EventSLABreached:
		if tc.AssigneeEmail != "" {
			return one(tc.AssigneeEmail, TemplateSLABreached, base)
		}
		return one(tc.SupportEmail, TemplateSLABreached, base)

	case EventTicketStatusChanged:
		// Only the transition into solved produces a customer email; it is
		// the one that carries the satisfaction rating link.
		if !isSolvedEvent(p.Data) {
			return nil
		}
		base.ReplyTo = tc.SupportEmail
		return one(tc.RequesterEmail, TemplateTicketSolved, base)
	}

	return nil
}

func one(to, template string, payload DeliveryPayload) []PlannedDelivery {
	to = strings.TrimSpace(to)
	if to == "" {
		return nil
	}
	return []PlannedDelivery{{ToEmail: to, Template: template, Payload: payload}}
}
