package search

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

// Backend is a search implementation. The Postgres repository implements it
// today; the Elasticsearch backend implements it in a later step. The service
// owns highlight rendering and org-id enforcement so every backend gets the
// same guarantees.
type Backend interface {
	Search(ctx context.Context, orgID uuid.UUID, f Filter) (Result, error)
}

// Service is the search boundary.
type Service struct {
	repo Backend
}

// NewService wires the search service.
func NewService(repo Backend) *Service {
	return &Service{repo: repo}
}

// Search runs a query for the caller's org. The org id always comes from the
// authenticated principal, never from user input.
func (s *Service) Search(ctx context.Context, p requestctx.Principal, f Filter) (Result, error) {
	if p.OrgID == uuid.Nil {
		return Result{}, ErrMissingOrg
	}
	f.Query = strings.TrimSpace(f.Query)
	f.Limit = NormalizeLimit(f.Limit)

	res, err := s.repo.Search(ctx, p.OrgID, f)
	if err != nil {
		return Result{}, err
	}
	for i := range res.Hits {
		res.Hits[i].SubjectHighlight = RenderHighlight(res.Hits[i].SubjectHighlight)
		res.Hits[i].CommentHighlight = RenderHighlight(res.Hits[i].CommentHighlight)
	}
	return res, nil
}
