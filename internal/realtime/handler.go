package realtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/platform/httpx"
	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

const (
	defaultWriteTimeout = 10 * time.Second
	eventNameTicket     = "ticket"
)

// Tokens is the token side of the handler. TokenStore implements it.
type Tokens interface {
	Issue(ctx context.Context, p requestctx.Principal) (string, time.Time, error)
	Consume(ctx context.Context, token string) (requestctx.Principal, error)
}

// Streamer is the hub side of the handler.
type Streamer interface {
	Serve(ctx context.Context, orgID uuid.UUID, afterSeq int64, sink Sink) error
}

// TypingBroadcaster is the typing side of the handler.
type TypingBroadcaster interface {
	Typing(ctx context.Context, orgID uuid.UUID, number int64, actorID uuid.UUID) error
}

// Handler exposes the stream token, SSE stream and typing endpoints.
type Handler struct {
	tokens  Tokens
	hub     Streamer
	typing  TypingBroadcaster
	logger  *slog.Logger
	timeout time.Duration
}

func NewHandler(tokens Tokens, hub Streamer, typing TypingBroadcaster, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{
		tokens:  tokens,
		hub:     hub,
		typing:  typing,
		logger:  logger,
		timeout: defaultWriteTimeout,
	}
}

// StreamTokenResponse is the short-lived credential used to open an SSE
// connection.
type StreamTokenResponse struct {
	Token     string    `json:"token" example:"Vt3..."`
	ExpiresAt time.Time `json:"expires_at"`
}

// IssueStreamToken mints a single-use token for EventSource.
//
//	@Summary		Issue a stream token
//	@Description	EventSource cannot send an Authorization header, so an authenticated call mints a random token that is valid for 30 seconds and can be used once. The token is bound to the caller's org; the stream can never read another tenant's events.
//	@Tags			realtime
//	@Produce		json
//	@Security		BearerAuth
//	@Success		201	{object}	StreamTokenResponse
//	@Failure		401	{object}	httpx.APIError
//	@Router			/v1/stream-tokens [post]
func (h *Handler) IssueStreamToken(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, httpx.Unauthorized("unauthorized", "authentication required"))
		return
	}

	token, expiresAt, err := h.tokens.Issue(r.Context(), p)
	if err != nil {
		httpx.WriteError(w, r, httpx.InternalError(err))
		return
	}
	_ = httpx.WriteJSON(w, http.StatusCreated, StreamTokenResponse{
		Token:     token,
		ExpiresAt: expiresAt,
	})
}

// Stream opens the org's Server-Sent Events feed.
//
//	@Summary		Live ticket stream
//	@Description	Server-Sent Events for the org bound to the stream token. The token is single-use and passed in the query string because EventSource cannot set headers. Each ticket event uses its ticket_events seq as the SSE id; reconnect with Last-Event-ID to replay only the missed events. A comment heartbeat is sent every 20 seconds.
//	@Tags			realtime
//	@Produce		text/event-stream
//	@Param			token			query	string	true	"Single-use token from POST /v1/stream-tokens"
//	@Param			Last-Event-ID	header	string	false	"Last seq the client received"
//	@Success		200				{string}	string	"SSE stream"
//	@Failure		401				{object}	httpx.APIError
//	@Router			/v1/stream [get]
func (h *Handler) Stream(w http.ResponseWriter, r *http.Request) {
	p, err := h.tokens.Consume(r.Context(), r.URL.Query().Get("token"))
	if err != nil {
		if errors.Is(err, ErrInvalidToken) {
			httpx.WriteError(w, r, httpx.Unauthorized("invalid_stream_token", "stream token is invalid, expired, or already used"))
			return
		}
		httpx.WriteError(w, r, httpx.InternalError(err))
		return
	}

	afterSeq, err := lastEventID(r)
	if err != nil {
		httpx.WriteError(w, r, httpx.BadRequest("invalid_last_event_id", "Last-Event-ID must be a non-negative integer"))
		return
	}

	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-cache, no-transform")
	header.Set("Connection", "keep-alive")
	header.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	rc := http.NewResponseController(w)
	// Streams outlive the server's normal write timeout. The hub sets its own
	// per-write deadline so a client that stops reading cannot pin a writer
	// forever.
	if err := rc.SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		h.logger.Debug("stream write deadline could not be cleared", "error", err)
	}
	if err := rc.Flush(); err != nil {
		return
	}

	sink := &sseSink{writer: w, rc: rc, timeout: h.timeout}
	if err := h.hub.Serve(r.Context(), p.OrgID, afterSeq, sink); err != nil && r.Context().Err() == nil {
		h.logger.Debug("stream closed", "org_id", p.OrgID, "error", err)
	}
}

// Typing publishes an ephemeral typing indicator.
//
//	@Summary		Signal that an agent is typing
//	@Description	Sets a 5 second Redis key and publishes a typing event to the org stream. Clients show the indicator until events stop arriving. A ticket in another org returns 404.
//	@Tags			realtime
//	@Produce		json
//	@Security		BearerAuth
//	@Param			number	path	int	true	"Per-org ticket number"
//	@Success		204
//	@Failure		400	{object}	httpx.APIError
//	@Failure		401	{object}	httpx.APIError
//	@Failure		404	{object}	httpx.APIError
//	@Router			/v1/tickets/{number}/typing [post]
func (h *Handler) Typing(w http.ResponseWriter, r *http.Request) {
	p, ok := requestctx.FromContext(r.Context())
	if !ok {
		httpx.WriteError(w, r, httpx.Unauthorized("unauthorized", "authentication required"))
		return
	}

	number, err := strconv.ParseInt(chi.URLParam(r, "number"), 10, 64)
	if err != nil || number <= 0 {
		httpx.WriteError(w, r, httpx.BadRequest("invalid_number", "ticket number must be a positive integer"))
		return
	}

	if err := h.typing.Typing(r.Context(), p.OrgID, number, p.ActorID); err != nil {
		if errors.Is(err, ErrTicketNotFound) {
			httpx.WriteError(w, r, httpx.NotFound("ticket_not_found", "ticket not found"))
			return
		}
		httpx.WriteError(w, r, httpx.InternalError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func lastEventID(r *http.Request) (int64, error) {
	raw := r.Header.Get("Last-Event-ID")
	if raw == "" {
		raw = r.URL.Query().Get("last_event_id")
	}
	if raw == "" {
		return 0, nil
	}
	seq, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || seq < 0 {
		return 0, fmt.Errorf("invalid Last-Event-ID %q", raw)
	}
	return seq, nil
}

// sseSink writes one SSE frame per event. The mutex-free design relies on the
// hub calling it from a single handler goroutine.
type sseSink struct {
	writer  io.Writer
	rc      *http.ResponseController
	timeout time.Duration
}

func (s *sseSink) Event(ev Event) error {
	payload, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("marshal stream event: %w", err)
	}

	name := eventNameTicket
	if ev.Seq == 0 {
		name = ev.Type
	}

	var frame []byte
	if ev.Seq > 0 {
		frame = []byte(fmt.Sprintf("id: %d\nevent: %s\ndata: %s\n\n", ev.Seq, name, payload))
	} else {
		frame = []byte(fmt.Sprintf("event: %s\ndata: %s\n\n", name, payload))
	}
	return s.write(frame)
}

func (s *sseSink) Heartbeat() error {
	return s.write([]byte(": heartbeat\n\n"))
}

func (s *sseSink) write(frame []byte) error {
	if s.rc != nil {
		_ = s.rc.SetWriteDeadline(time.Now().Add(s.timeout))
	}
	if _, err := s.writer.Write(frame); err != nil {
		return err
	}
	if s.rc != nil {
		return s.rc.Flush()
	}
	return nil
}
