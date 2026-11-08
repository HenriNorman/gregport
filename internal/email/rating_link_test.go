package email

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/ratings"
)

func solvedPayload(tc TicketContext, newStatus string) TaskPayload {
	return TaskPayload{
		EventID:   uuid.New(),
		OrgID:     tc.OrgID,
		TicketID:  tc.TicketID,
		EventType: EventTicketStatusChanged,
		Data:      json.RawMessage(`{"old":"open","new":"` + newStatus + `"}`),
	}
}

func TestPlanSolvedEventIncludesRatingLink(t *testing.T) {
	repo := &fakeRepo{tickets: map[uuid.UUID]TicketContext{}}
	tk := ticketContext()
	repo.tickets[tk.TicketID] = tk

	tokens := ratings.NewTokenManager(strings.Repeat("r", 32), time.Hour)
	svc := NewService(repo)
	svc.SetRatingLinks(tokens, "https://support.example.com/")

	ds, err := svc.Plan(context.Background(), solvedPayload(tk, "solved"))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(ds) != 1 {
		t.Fatalf("deliveries: %+v", ds)
	}
	if ds[0].ToEmail != tk.RequesterEmail || ds[0].Template != TemplateTicketSolved {
		t.Fatalf("delivery: %+v", ds[0])
	}

	var stored DeliveryPayload
	if err := json.Unmarshal(ds[0].Payload, &stored); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if stored.ReplyTo != tk.SupportEmail {
		t.Fatalf("reply-to=%q", stored.ReplyTo)
	}

	link, err := url.Parse(stored.RatingURL)
	if err != nil {
		t.Fatalf("rating url: %v", err)
	}
	if link.Scheme != "https" || link.Host != "support.example.com" || link.Path != "/rate" {
		t.Fatalf("rating url: %s", stored.RatingURL)
	}
	claims, err := tokens.Verify(link.Query().Get("token"))
	if err != nil {
		t.Fatalf("verify token: %v", err)
	}
	if claims.TicketID != tk.TicketID || claims.CustomerID != tk.RequesterID {
		t.Fatalf("claims: %+v", claims)
	}
}

func TestPlanNonSolvedStatusChangeCreatesNothing(t *testing.T) {
	repo := &fakeRepo{tickets: map[uuid.UUID]TicketContext{}}
	tk := ticketContext()
	repo.tickets[tk.TicketID] = tk

	svc := NewService(repo)
	svc.SetRatingLinks(ratings.NewTokenManager(strings.Repeat("r", 32), time.Hour), "https://support.example.com")

	for _, status := range []string{"open", "pending", "closed"} {
		ds, err := svc.Plan(context.Background(), solvedPayload(tk, status))
		if err != nil {
			t.Fatalf("%s: %v", status, err)
		}
		if len(ds) != 0 {
			t.Fatalf("%s: unexpected deliveries %+v", status, ds)
		}
	}
}

func TestPlanSolvedEventWithoutRatingLinksStillSendsEmail(t *testing.T) {
	repo := &fakeRepo{tickets: map[uuid.UUID]TicketContext{}}
	tk := ticketContext()
	repo.tickets[tk.TicketID] = tk

	ds, err := NewService(repo).Plan(context.Background(), solvedPayload(tk, "solved"))
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(ds) != 1 || ds[0].Template != TemplateTicketSolved {
		t.Fatalf("deliveries: %+v", ds)
	}
	var stored DeliveryPayload
	if err := json.Unmarshal(ds[0].Payload, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.RatingURL != "" {
		t.Fatalf("unexpected rating url: %q", stored.RatingURL)
	}
}

func TestPlanSolvedEventIsIdempotent(t *testing.T) {
	repo := &fakeRepo{tickets: map[uuid.UUID]TicketContext{}}
	tk := ticketContext()
	repo.tickets[tk.TicketID] = tk

	svc := NewService(repo)
	svc.SetRatingLinks(ratings.NewTokenManager(strings.Repeat("r", 32), time.Hour), "https://support.example.com")
	p := solvedPayload(tk, "solved")

	first, err := svc.Plan(context.Background(), p)
	if err != nil {
		t.Fatalf("first plan: %v", err)
	}
	second, err := svc.Plan(context.Background(), p)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("deliveries: %+v / %+v", first, second)
	}
	if first[0].ID != second[0].ID {
		t.Fatalf("replay created a new delivery: %s vs %s", first[0].ID, second[0].ID)
	}
	if len(repo.deliveries) != 1 {
		t.Fatalf("expected one persisted delivery, got %d", len(repo.deliveries))
	}
}

func TestRenderSolvedEmailRatingLink(t *testing.T) {
	ratingURL := "https://support.example.com/rate?token=abc.def"
	rendered, err := renderEmail(TemplateTicketSolved, DeliveryPayload{
		OrgName:       "Acme",
		TicketNumber:  1042,
		Subject:       "Cannot log in",
		RequesterName: "Ada",
		ReplyTo:       "support@acme.test",
		RatingURL:     ratingURL,
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !strings.Contains(rendered.Text, ratingURL) {
		t.Fatalf("text missing rating link: %q", rendered.Text)
	}
	if !strings.Contains(rendered.HTML, ratingURL) {
		t.Fatalf("html missing rating link: %q", rendered.HTML)
	}
	if !strings.Contains(rendered.Text, "has been solved") || !strings.Contains(rendered.HTML, "has been solved") {
		t.Fatalf("solved copy missing: %+v", rendered)
	}

	empty, err := renderEmail(TemplateTicketSolved, DeliveryPayload{
		OrgName: "Acme", TicketNumber: 7, Subject: "Done",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(empty.Text, "Rate this ticket") || strings.Contains(empty.HTML, "Rate this ticket") {
		t.Fatalf("unexpected rating call to action without a link: %+v", empty)
	}
}

func TestRenderSolvedEmailEscapesRatingURL(t *testing.T) {
	rendered, err := renderEmail(TemplateTicketSolved, DeliveryPayload{
		OrgName:      "Acme",
		TicketNumber: 7,
		Subject:      "Done",
		RatingURL:    `https://example.com/rate?token="><script>alert(1)</script>`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(rendered.HTML, "<script>") {
		t.Fatalf("rating url was not escaped in html: %s", rendered.HTML)
	}
}
