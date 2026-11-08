package webhooks

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testSubscription(t *testing.T, url string) Subscription {
	t.Helper()
	return Subscription{ID: uuid.New(), URL: url, Secret: "whsec_test_secret", IsActive: true}
}

func testDelivery(payload string) Delivery {
	return Delivery{
		ID:        uuid.New(),
		EventType: EventTicketCreated,
		Payload:   []byte(payload),
	}
}

func TestHTTPSenderSignsRequest(t *testing.T) {
	body := `{"id":"x","type":"ticket.created","created_at":"2026-01-15T10:00:00Z","data":{}}`
	var got *http.Request
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r
		gotBody, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	sub := testSubscription(t, server.URL)
	delivery := testDelivery(body)
	sender := NewHTTPSender(SenderOptions{AllowPrivate: true})
	resp := sender.Send(context.Background(), sub, delivery)
	if !resp.Success() {
		t.Fatalf("response: %+v", resp)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if got.Method != http.MethodPost {
		t.Fatalf("method=%s", got.Method)
	}
	if got.Header.Get("Content-Type") != "application/json" {
		t.Fatalf("content-type=%q", got.Header.Get("Content-Type"))
	}
	if got.Header.Get(HeaderEvent) != EventTicketCreated {
		t.Fatalf("event header=%q", got.Header.Get(HeaderEvent))
	}
	if got.Header.Get(HeaderDelivery) != delivery.ID.String() {
		t.Fatalf("delivery header=%q", got.Header.Get(HeaderDelivery))
	}
	if got.Header.Get(HeaderAttempt) != "1" {
		t.Fatalf("attempt header=%q", got.Header.Get(HeaderAttempt))
	}
	timestamp := got.Header.Get(HeaderTimestamp)
	if timestamp == "" {
		t.Fatal("missing timestamp")
	}
	want := Sign(sub.Secret, timestamp, gotBody)
	if got.Header.Get(HeaderSignature) != want {
		t.Fatalf("signature=%q want=%q", got.Header.Get(HeaderSignature), want)
	}
	if string(gotBody) != body {
		t.Fatalf("body=%s", gotBody)
	}
}

func TestHTTPSenderClassifiesResponses(t *testing.T) {
	tests := []struct {
		status int
		body   string
		want   bool
	}{
		{status: http.StatusOK, body: "ok", want: true},
		{status: http.StatusCreated, body: "ok", want: true},
		{status: http.StatusNoContent, want: true},
		{status: http.StatusMovedPermanently, body: "moved"},
		{status: http.StatusNotFound, body: "nope"},
		{status: http.StatusInternalServerError, body: "boom"},
	}

	for _, tt := range tests {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			sender := NewHTTPSender(SenderOptions{AllowPrivate: true})
			resp := sender.Send(context.Background(), testSubscription(t, server.URL), testDelivery(`{}`))
			if resp.Success() != tt.want {
				t.Fatalf("success=%v status=%d err=%v", resp.Success(), resp.StatusCode, resp.Err)
			}
			if resp.StatusCode != tt.status {
				t.Fatalf("status=%d want=%d", resp.StatusCode, tt.status)
			}
			if string(resp.Body) != tt.body {
				t.Fatalf("body=%q want=%q", resp.Body, tt.body)
			}
		})
	}
}

func TestHTTPSenderDoesNotFollowRedirects(t *testing.T) {
	var hits int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.URL.Path == "/hook" {
			http.Redirect(w, r, "/internal", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	sender := NewHTTPSender(SenderOptions{AllowPrivate: true})
	resp := sender.Send(context.Background(), testSubscription(t, server.URL+"/hook"), testDelivery(`{}`))
	if resp.Success() {
		t.Fatal("redirect should not be followed as success")
	}
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if hits != 1 {
		t.Fatalf("redirect target was contacted: hits=%d", hits)
	}
}

func TestHTTPSenderTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(150 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	sender := NewHTTPSender(SenderOptions{AllowPrivate: true, Timeout: 25 * time.Millisecond})
	resp := sender.Send(context.Background(), testSubscription(t, server.URL), testDelivery(`{}`))
	if resp.Success() {
		t.Fatal("timed-out request reported success")
	}
	if resp.Err == nil {
		t.Fatal("expected a transport error")
	}
}

func TestSign(t *testing.T) {
	first := Sign("secret", "1700000000", []byte(`{"a":1}`))
	second := Sign("secret", "1700000000", []byte(`{"a":1}`))
	if first != second || first == "" {
		t.Fatalf("signature is not deterministic: %q %q", first, second)
	}
	if Sign("other", "1700000000", []byte(`{"a":1}`)) == first {
		t.Fatal("different secret produced the same signature")
	}
	if Sign("secret", "1700000001", []byte(`{"a":1}`)) == first {
		t.Fatal("different timestamp produced the same signature")
	}
}

func TestHTTPSenderRejectsPrivateDial(t *testing.T) {
	// Production mode must refuse a loopback URL even though httptest only
	// listens on loopback.
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	sender := NewHTTPSender(SenderOptions{Timeout: time.Second})
	resp := sender.Send(context.Background(), testSubscription(t, server.URL), testDelivery(`{}`))
	if resp.Success() {
		t.Fatal("loopback destination was delivered")
	}
	if resp.Err == nil {
		t.Fatal("expected a dial error")
	}
}
