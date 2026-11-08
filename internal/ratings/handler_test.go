package ratings

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func request(t *testing.T, svc *Service, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/public/ratings", bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	NewHandler(svc).Submit(rec, req)
	return rec
}

func decodeErrorCode(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error %q: %v", rec.Body.String(), err)
	}
	return envelope.Error.Code
}

func TestHandlerSubmitCreated(t *testing.T) {
	svc, _, _, tokens, ticket := serviceFixture(t)
	token := tokenFor(t, tokens, ticket)

	rec := request(t, svc, `{"token":"`+token+`","score":5,"comment":"Great"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	var resp RatingResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.TicketNumber != ticket.Number || resp.Score != 5 || resp.Comment != "Great" {
		t.Fatalf("response: %+v", resp)
	}
	if strings.Contains(rec.Body.String(), ticket.OrgID.String()) {
		t.Fatalf("response leaked the org id: %s", rec.Body.String())
	}
}

func TestHandlerSubmitErrors(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		mutate   func(*fakeRepo)
		wantCode int
		wantErr  string
	}{
		{
			name:     "malformed json",
			body:     `{"score":`,
			wantCode: http.StatusBadRequest,
		},
		{
			name:     "missing score",
			body:     `{"token":"x","score":0}`,
			wantCode: http.StatusUnprocessableEntity,
			wantErr:  "invalid_score",
		},
		{
			name:     "tampered token",
			body:     `{"token":"garbage","score":5}`,
			wantCode: http.StatusUnprocessableEntity,
			wantErr:  "invalid_token",
		},
		{
			name:     "already rated",
			body:     `{"token":"valid","score":5}`,
			mutate:   func(r *fakeRepo) { r.insertErr = ErrAlreadyRated },
			wantCode: http.StatusConflict,
			wantErr:  "already_rated",
		},
		{
			name:     "no such ticket",
			body:     `{"token":"valid","score":5}`,
			mutate:   func(r *fakeRepo) { r.loadErr = ErrTicketNotFound },
			wantCode: http.StatusUnprocessableEntity,
			wantErr:  "invalid_token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, repo, _, tokens, ticket := serviceFixture(t)
			if tt.mutate != nil {
				tt.mutate(repo)
			}
			body := tt.body
			if strings.Contains(body, `"valid"`) {
				body = strings.Replace(body, `"valid"`, `"`+tokenFor(t, tokens, ticket)+`"`, 1)
			}
			rec := request(t, svc, body)
			if rec.Code != tt.wantCode {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if tt.wantErr != "" && decodeErrorCode(t, rec) != tt.wantErr {
				t.Fatalf("code=%q body=%s", decodeErrorCode(t, rec), rec.Body.String())
			}
		})
	}
}

func TestHandlerSubmitExpiredToken(t *testing.T) {
	svc, _, _, tokens, ticket := serviceFixture(t)
	base := time.Now()
	tokens.now = func() time.Time { return base }
	token := tokenFor(t, tokens, ticket)
	tokens.now = func() time.Time { return base.Add(2 * time.Hour) }

	rec := request(t, svc, `{"token":"`+token+`","score":5}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if code := decodeErrorCode(t, rec); code != "expired_token" {
		t.Fatalf("code=%q", code)
	}
}

func TestHandlerSubmitOversizedComment(t *testing.T) {
	svc, _, _, tokens, ticket := serviceFixture(t)
	token := tokenFor(t, tokens, ticket)
	body, err := json.Marshal(CreateRequest{
		Token:   token,
		Score:   5,
		Comment: strings.Repeat("x", MaxCommentLength+1),
	})
	if err != nil {
		t.Fatal(err)
	}

	rec := request(t, svc, string(body))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if code := decodeErrorCode(t, rec); code != "invalid_comment" {
		t.Fatalf("code=%q", code)
	}
}

func TestHandlerSubmitDoesNotRequireAuth(t *testing.T) {
	svc, _, _, tokens, ticket := serviceFixture(t)
	token := tokenFor(t, tokens, ticket)

	// No Authorization header is set; a signed token is the only credential.
	rec := request(t, svc, `{"token":"`+token+`","score":3}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
