package inboundemail

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Service processes an inbound email: verifies the provider signature,
// deduplicates on Message-ID, resolves the org, and routes the message to a
// new ticket or a comment on an existing ticket.
type Service struct {
	repo      Repo
	secret    string
	tolerance time.Duration
	now       func() time.Time
}

// NewService creates the inbound email service. secret is the HMAC key shared
// with the email provider for signature verification.
func NewService(repo Repo, secret string) *Service {
	return &Service{
		repo:      repo,
		secret:    secret,
		tolerance: DefaultSignatureTolerance,
		now:       time.Now,
	}
}

// SetTolerance overrides the signature timestamp tolerance.
func (s *Service) SetTolerance(d time.Duration) {
	if d > 0 {
		s.tolerance = d
	}
}

// Process handles one inbound email webhook from the provider. rawBody is the
// exact bytes that were signed; timestamp and signature come from the
// X-Inbound-Email-Timestamp and X-Inbound-Email-Signature headers.
//
// A malformed or unverifiable message returns a permanent error so the
// provider does not redeliver it. A duplicate Message-ID is idempotent and
// returns nil.
func (s *Service) Process(ctx context.Context, rawBody []byte, timestamp, signature string) error {
	if s.secret == "" {
		return errors.New("inboundemail: signature secret not configured")
	}
	if err := VerifyTimestamp(timestamp, s.now().UTC(), s.tolerance); err != nil {
		return err
	}
	if !VerifySignature(s.secret, timestamp, rawBody, []byte(signature)) {
		return ErrInvalidSignature
	}

	e, err := Parse(rawBody)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	if err := e.validate(); err != nil {
		return err
	}

	orgID, err := s.repo.ResolveOrg(ctx, e.Recipient)
	if err != nil {
		return err
	}

	// Idempotency: the same Message-ID must never produce two deliveries.
	_, findErr := s.repo.FindByMessageID(ctx, e.MessageID)
	switch {
	case findErr == nil:
		// Already processed: record as a duplicate and stop.
		if insertErr := s.repo.InsertInbound(ctx, orgID, *e, "duplicate", nil, nil); insertErr != nil &&
			!errors.Is(insertErr, ErrDuplicateMessageID) {
			return insertErr
		}
		return nil
	case !errors.Is(findErr, pgx.ErrNoRows):
		return findErr
	}

	customerID, err := s.repo.FindOrCreateCustomer(ctx, orgID, e.FromEmail, e.FromName)
	if err != nil {
		return fmt.Errorf("resolve customer: %w", err)
	}

	// Route: [#1042] in subject, then In-Reply-To, then a brand-new ticket.
	if number := TicketNumberFromSubject(e.Subject); number > 0 {
		ticketID, err := s.repo.FindTicketByNumber(ctx, orgID, number)
		if err != nil {
			return err
		}
		if err := s.repo.AddComment(ctx, orgID, ticketID, customerID, e.Body); err != nil {
			return fmt.Errorf("add comment: %w", err)
		}
		if err := s.record(ctx, orgID, *e, "comment_added", &ticketID, nil); err != nil {
			return err
		}
		return nil
	}

	if e.InReplyTo != "" {
		ref, err := s.repo.FindByMessageID(ctx, e.InReplyTo)
		if err == nil && ref.TicketID != nil {
			if err := s.repo.AddComment(ctx, orgID, *ref.TicketID, customerID, e.Body); err != nil {
				return fmt.Errorf("add comment: %w", err)
			}
			if err := s.record(ctx, orgID, *e, "comment_added", ref.TicketID, nil); err != nil {
				return err
			}
			return nil
		}
		// In-Reply-To points at a message we never received; fall through to
		// creating a new ticket rather than dropping the mail.
	}

	ticket, err := s.repo.CreateTicket(ctx, orgID, customerID, e.FromEmail, e.FromName, e.Subject, e.Body)
	if err != nil {
		return fmt.Errorf("create ticket: %w", err)
	}
	if err := s.record(ctx, orgID, *e, "ticket_created", &ticket.ID, nil); err != nil {
		return err
	}
	return nil
}

// record persists the inbound email row, linking it to the ticket or comment
// it produced. A concurrent duplicate insert is treated as idempotent.
func (s *Service) record(ctx context.Context, orgID uuid.UUID, e InboundEmail, status string, ticketID, commentID *uuid.UUID) error {
	if err := s.repo.InsertInbound(ctx, orgID, e, status, ticketID, commentID); err != nil {
		if errors.Is(err, ErrDuplicateMessageID) {
			return nil
		}
		return err
	}
	return nil
}
