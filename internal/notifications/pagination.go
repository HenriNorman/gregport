package notifications

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ErrInvalidCursor is returned when a pagination cursor cannot be decoded.
var ErrInvalidCursor = errors.New("notifications: invalid cursor")

// cursor is the opaque (created_at, id) keyset position for notification
// pagination. It is intentionally the same shape as the ticket cursor.
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
		return cursor{}, fmt.Errorf("%w: decode: %v", ErrInvalidCursor, err)
	}
	var c cursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return cursor{}, fmt.Errorf("%w: unmarshal: %v", ErrInvalidCursor, err)
	}
	if c.CreatedAt.IsZero() || c.ID == uuid.Nil {
		return cursor{}, fmt.Errorf("%w: missing fields", ErrInvalidCursor)
	}
	return c, nil
}
