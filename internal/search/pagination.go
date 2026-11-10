package search

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// cursor is the opaque pagination position. rank is the relevance score of
// the last hit, so the next page resumes strictly after it. created_at is the
// second sort key. An empty query sorts by created_at and stores rank zero.
//
// id is the Postgres tiebreaker, while number is the per-org ticket number
// used by Elasticsearch. Both are encoded so a cursor can be consumed by
// either backend (for example after falling back to Postgres).
type cursor struct {
	Rank      float64   `json:"r"`
	CreatedAt time.Time `json:"t"`
	ID        uuid.UUID `json:"i"`
	Number    int64     `json:"n"`
}

func encodeCursor(rank float64, createdAt time.Time, id uuid.UUID, number int64) string {
	raw, _ := json.Marshal(cursor{Rank: rank, CreatedAt: createdAt, ID: id, Number: number})
	return base64.RawURLEncoding.EncodeToString(raw)
}

func decodeCursor(s string) (cursor, error) {
	if s == "" {
		return cursor{}, nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return cursor{}, fmt.Errorf("decode search cursor: %w", err)
	}
	var c cursor
	if err := json.Unmarshal(raw, &c); err != nil {
		return cursor{}, fmt.Errorf("unmarshal search cursor: %w", err)
	}
	if c.CreatedAt.IsZero() || c.ID == uuid.Nil {
		return cursor{}, fmt.Errorf("search cursor missing fields")
	}
	return c, nil
}
