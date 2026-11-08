package webhooks

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/queue"
)

// ErrNoSender is returned when a delivery is requested without a configured
// HTTP sender. It is an internal wiring error, never a client error.
var ErrNoSender = errors.New("webhooks: sender is not configured")

// Enqueuer is the queue side of the dispatcher. queue.Client satisfies it.
type Enqueuer interface {
	EnqueueJob(ctx context.Context, job queue.Job) error
}

// Service validates subscriptions, plans deliveries and performs attempts.
type Service struct {
	repo             Store
	sender           Sender
	enqueuer         Enqueuer
	resolver         Resolver
	failureThreshold int
	now              func() time.Time
}

// NewService creates the webhook service. The production sender and resolver
// are installed by SetSender; without a sender the HTTP endpoints that contact
// receivers fail with ErrNoSender rather than silently succeeding.
func NewService(repo Store) *Service {
	return &Service{
		repo:             repo,
		resolver:         net.DefaultResolver,
		failureThreshold: DefaultFailureThreshold,
		now:              time.Now,
	}
}

// SetSender installs the HTTP sender used by test, redelivery and worker
// attempts.
func (s *Service) SetSender(sender Sender) { s.sender = sender }

// SetEnqueuer installs the queue client used to schedule delivery attempts.
func (s *Service) SetEnqueuer(enqueuer Enqueuer) { s.enqueuer = enqueuer }

// SetResolver overrides DNS resolution for URL validation. Tests use it to
// make hostname checks deterministic.
func (s *Service) SetResolver(resolver Resolver) {
	if resolver != nil {
		s.resolver = resolver
	}
}

// SetFailureThreshold overrides how many consecutive failures disable a
// subscription. Non-positive values keep the default.
func (s *Service) SetFailureThreshold(n int) {
	if n > 0 {
		s.failureThreshold = n
	}
}

// List returns every subscription in the caller's org.
func (s *Service) List(ctx context.Context, orgID uuid.UUID) ([]Subscription, error) {
	return s.repo.List(ctx, orgID)
}

// Get returns one subscription in the caller's org.
func (s *Service) Get(ctx context.Context, orgID, id uuid.UUID) (Subscription, error) {
	return s.repo.Get(ctx, orgID, id)
}

// Create validates and stores a subscription. The generated HMAC secret is
// only visible on the returned value (and therefore the create response).
func (s *Service) Create(ctx context.Context, orgID uuid.UUID, req CreateSubscriptionRequest) (Subscription, error) {
	url, err := ValidateDestination(ctx, req.URL, s.resolver)
	if err != nil {
		return Subscription{}, err
	}
	events, err := normalizeEvents(req.Events)
	if err != nil {
		return Subscription{}, err
	}
	secret, err := newSecret()
	if err != nil {
		return Subscription{}, err
	}
	return s.repo.Create(ctx, orgID, Subscription{
		URL:    url,
		Events: events,
		Secret: secret,
	})
}

// Update applies a partial update. A disabled subscription re-enabled through
// this call starts with a clean failure counter.
func (s *Service) Update(ctx context.Context, orgID, id uuid.UUID, req UpdateSubscriptionRequest) (Subscription, error) {
	current, err := s.repo.Get(ctx, orgID, id)
	if err != nil {
		return Subscription{}, err
	}

	next := current
	if req.URL != nil {
		url, err := ValidateDestination(ctx, *req.URL, s.resolver)
		if err != nil {
			return Subscription{}, err
		}
		next.URL = url
	}
	if req.Events != nil {
		events, err := normalizeEvents(*req.Events)
		if err != nil {
			return Subscription{}, err
		}
		next.Events = events
	}
	if req.IsActive != nil {
		next.IsActive = *req.IsActive
	}
	resetFailures := !current.IsActive && next.IsActive
	return s.repo.Update(ctx, orgID, next, resetFailures)
}

// Delete removes a subscription and its delivery history.
func (s *Service) Delete(ctx context.Context, orgID, id uuid.UUID) error {
	return s.repo.Delete(ctx, orgID, id)
}

// ListDeliveries returns the delivery log for one subscription, newest first.
func (s *Service) ListDeliveries(ctx context.Context, orgID, subscriptionID uuid.UUID, limit int, cursor string) ([]Delivery, string, error) {
	if _, err := s.repo.Get(ctx, orgID, subscriptionID); err != nil {
		return nil, "", err
	}
	return s.repo.ListDeliveries(ctx, orgID, subscriptionID, limit, cursor)
}

// Test sends a synchronous ping to the subscription and records it in the
// delivery log. A failed ping is still returned successfully; its Status and
// response metadata explain what happened. Test attempts do not count toward
// the circuit breaker.
func (s *Service) Test(ctx context.Context, orgID, id uuid.UUID) (Delivery, error) {
	sub, err := s.repo.Get(ctx, orgID, id)
	if err != nil {
		return Delivery{}, err
	}
	if !sub.IsActive {
		return Delivery{}, ErrInactive
	}
	if s.sender == nil {
		return Delivery{}, ErrNoSender
	}

	envelope := Envelope{
		ID:        uuid.New(),
		Type:      EventPing,
		CreatedAt: s.now().UTC(),
		Data: EnvelopeData{
			Event: json.RawMessage(`{"message":"This is a test webhook from Gregport"}`),
		},
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return Delivery{}, fmt.Errorf("marshal test envelope: %w", err)
	}

	delivery, _, err := s.repo.InsertDelivery(ctx, Delivery{
		OrgID:          orgID,
		SubscriptionID: sub.ID,
		EventType:      EventPing,
		Payload:        payload,
	})
	if err != nil {
		return Delivery{}, err
	}
	return s.attempt(ctx, sub, delivery, false, false)
}

// Redeliver resets a delivery-log row and performs one immediate manual
// attempt. It does not count toward the subscription's circuit breaker and does
// not schedule an automatic retry; an admin can redeliver again.
func (s *Service) Redeliver(ctx context.Context, orgID, subscriptionID, deliveryID uuid.UUID) (Delivery, error) {
	if s.sender == nil {
		return Delivery{}, ErrNoSender
	}
	delivery, sub, err := s.repo.ResetDeliveryForRedelivery(ctx, orgID, subscriptionID, deliveryID)
	if err != nil {
		if errors.Is(err, ErrDeliveryGone) {
			return Delivery{}, ErrDeliveryNotFound
		}
		return Delivery{}, err
	}
	return s.attempt(ctx, sub, delivery, false, false)
}

// Dispatch plans one delivery per matching active subscription and enqueues a
// delivery task for each. The unique (subscription_id, event_id) index makes
// replay converge on the existing row.
func (s *Service) Dispatch(ctx context.Context, event OutboxEvent) error {
	if err := event.Validate(); err != nil {
		return err
	}
	if !ValidEventType(event.EventType) {
		return nil
	}
	if s.enqueuer == nil {
		return ErrNoSender
	}

	subs, err := s.repo.ListActiveForEvent(ctx, event.OrgID, event.EventType)
	if err != nil {
		return err
	}
	if len(subs) == 0 {
		return nil
	}

	var ticket *TicketSummary
	summary, err := s.repo.LoadTicketSummary(ctx, event.OrgID, event.TicketID)
	switch {
	case err == nil:
		ticket = &summary
	case errors.Is(err, ErrNotFound):
		ticket = nil
	default:
		return err
	}

	envelope := Envelope{
		ID:        event.EventID,
		Type:      event.EventType,
		CreatedAt: s.now().UTC(),
		Data: EnvelopeData{
			Ticket: ticket,
			Event:  event.Data,
		},
	}
	payload, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("marshal webhook envelope: %w", err)
	}

	for _, sub := range subs {
		delivery, _, err := s.repo.InsertDelivery(ctx, Delivery{
			OrgID:          event.OrgID,
			SubscriptionID: sub.ID,
			EventID:        &event.EventID,
			EventType:      event.EventType,
			Payload:        payload,
		})
		if err != nil {
			return err
		}
		if delivery.Status == StatusSent || delivery.Status == StatusDead {
			continue
		}
		if err := s.enqueuer.EnqueueJob(ctx, queue.Job{
			ID:       "webhook-delivery:" + delivery.ID.String(),
			Type:     queue.TaskWebhookDeliver,
			Queue:    queue.QueueDefault,
			Payload:  mustJSON(DeliveryTaskPayload{DeliveryID: delivery.ID}),
			MaxRetry: queue.WebhookMaxRetries(),
		}); err != nil {
			return fmt.Errorf("enqueue webhook delivery %s: %w", delivery.ID, err)
		}
	}
	return nil
}

// Deliver performs one worker attempt for a queued delivery. Already sent or
// dead deliveries are a no-op so at-least-once task delivery is safe.
func (s *Service) Deliver(ctx context.Context, deliveryID uuid.UUID) error {
	delivery, sub, err := s.repo.LoadDeliveryForAttempt(ctx, deliveryID)
	if err != nil {
		if errors.Is(err, ErrDeliveryGone) {
			return nil
		}
		return err
	}
	switch delivery.Status {
	case StatusSent, StatusDead:
		return nil
	}
	if s.sender == nil {
		return ErrNoSender
	}
	if !sub.IsActive {
		// The subscription was disabled after this delivery was planned. Do
		// not contact the receiver; close the row as dead.
		delivery.Status = StatusDead
		delivery.Attempts++
		msg := "webhook subscription is disabled"
		delivery.LastError = &msg
		delivery.NextRetryAt = nil
		_, err := s.repo.RecordDeliveryFailure(ctx, delivery, s.failureThreshold, false)
		return err
	}

	updated, err := s.attempt(ctx, sub, delivery, true, true)
	if err != nil {
		return err
	}
	if updated.Status == StatusFailed {
		reason := "delivery failed"
		if updated.LastError != nil {
			reason = *updated.LastError
		}
		return fmt.Errorf("webhook delivery %s failed: %s", updated.ID, reason)
	}
	return nil
}

// attempt performs one HTTP request and persists the outcome. The returned
// error covers storage failures only; an HTTP failure is represented by the
// returned delivery's failed/dead status.
func (s *Service) attempt(ctx context.Context, sub Subscription, delivery Delivery, countFailure, scheduleRetry bool) (Delivery, error) {
	delivery.Attempts++
	resp := s.sender.Send(ctx, sub, delivery)

	duration := int(resp.Duration.Milliseconds())
	delivery.DurationMS = &duration
	if resp.StatusCode != 0 {
		status := resp.StatusCode
		delivery.ResponseStatus = &status
	}
	if body := truncateBody(resp.Body); body != "" {
		delivery.ResponseBody = &body
	}

	if resp.Success() {
		delivery.Status = StatusSent
		delivery.LastError = nil
		delivery.NextRetryAt = nil
		if err := s.repo.RecordDeliverySuccess(ctx, delivery); err != nil {
			return Delivery{}, err
		}
		return delivery, nil
	}

	delivery.Status = StatusFailed
	delivery.NextRetryAt = nil
	if resp.Err != nil {
		msg := resp.Err.Error()
		delivery.LastError = &msg
	} else {
		msg := fmt.Sprintf("HTTP %d", resp.StatusCode)
		delivery.LastError = &msg
	}
	if delivery.Attempts >= queue.WebhookMaxAttempts() {
		delivery.Status = StatusDead
	} else if scheduleRetry {
		next := s.now().Add(queue.WebhookRetryDelay(delivery.Attempts)).UTC()
		delivery.NextRetryAt = &next
	}

	if _, err := s.repo.RecordDeliveryFailure(ctx, delivery, s.failureThreshold, countFailure); err != nil {
		return Delivery{}, err
	}
	return delivery, nil
}

func normalizeEvents(events []string) ([]string, error) {
	if len(events) == 0 || len(events) > MaxEventsPerSubscription {
		return nil, ErrInvalidEvents
	}
	seen := make(map[string]struct{}, len(events))
	out := make([]string, 0, len(events))
	for _, event := range events {
		event = strings.TrimSpace(event)
		if !ValidEventType(event) {
			return nil, ErrInvalidEvents
		}
		if _, ok := seen[event]; ok {
			continue
		}
		seen[event] = struct{}{}
		out = append(out, event)
	}
	if len(out) == 0 {
		return nil, ErrInvalidEvents
	}
	return out, nil
}

func newSecret() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate webhook secret: %w", err)
	}
	return "whsec_" + hex.EncodeToString(raw), nil
}

func truncateBody(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	text := strings.ToValidUTF8(string(body), "\uFFFD")
	if len(text) > MaxResponseBodyBytes {
		text = strings.ToValidUTF8(text[:MaxResponseBodyBytes], "")
	}
	return text
}

func mustJSON(v any) []byte {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return raw
}
