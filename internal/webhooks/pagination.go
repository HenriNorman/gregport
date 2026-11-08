package webhooks

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/platform/httpx"
)

// ErrInvalidCursor is returned when a delivery-log cursor is malformed.
var ErrInvalidCursor = httpx.BadRequest("invalid_cursor", "cursor is invalid")

type cursor struct {
	CreatedAt time.Time `json:"t"`
	ID        uuid.UUID `json:"i"`
}

func encodeCursor(createdAt time.Time, id uuid.UUID) string {
	raw, _ := json.Marshal(cursor{CreatedAt: createdAt, ID: id})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCursor(s string) (cursor, error) {
	if s == "" {
		return cursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return cursor{}, fmt.Errorf("%w: decode", ErrInvalidCursor)
	}
	var c cursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return cursor{}, fmt.Errorf("%w: unmarshal", ErrInvalidCursor)
	}
	if c.CreatedAt.IsZero() || c.ID == uuid.Nil {
		return cursor{}, fmt.Errorf("%w: missing fields", ErrInvalidCursor)
	}
	return c, nil
}
