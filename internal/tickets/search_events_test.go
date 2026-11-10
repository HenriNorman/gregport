package tickets

import (
	"context"
	"testing"
)

// TestIndexedChangesBumpVersion proves that every mutation which changes a
// ticket's searchable document also increments the ticket version. The search
// index uses the version as an Elasticsearch external version, so a missed
// bump would silently reject the index update.
func TestIndexedChangesBumpVersion(t *testing.T) {
	_, repo := newTestHandler(t)
	orgA, _, number := setupTwoOrgs(t, repo)
	agentID := seedMembership(t, repo, orgA)
	ctx := context.Background()
	actor := Actor{Type: "user", ID: &agentID}

	start, err := repo.GetTicket(ctx, orgA, number)
	if err != nil {
		t.Fatalf("get ticket: %v", err)
	}

	if _, err := repo.CreateTag(ctx, orgA, "billing"); err != nil {
		t.Fatalf("create tag: %v", err)
	}
	if err := repo.AttachTag(ctx, orgA, number, "billing", actor); err != nil {
		t.Fatalf("attach tag: %v", err)
	}
	afterTag, err := repo.GetTicket(ctx, orgA, number)
	if err != nil {
		t.Fatalf("get after tag: %v", err)
	}
	if afterTag.Version != start.Version+1 {
		t.Fatalf("tag version = %d, want %d", afterTag.Version, start.Version+1)
	}

	if _, err := repo.CreateComment(ctx, CreateCommentInput{
		OrgID: orgA, Number: number, Actor: actor, Body: "searchable text",
	}); err != nil {
		t.Fatalf("comment: %v", err)
	}
	afterComment, err := repo.GetTicket(ctx, orgA, number)
	if err != nil {
		t.Fatalf("get after comment: %v", err)
	}
	if afterComment.Version != afterTag.Version+1 {
		t.Fatalf("comment version = %d, want %d", afterComment.Version, afterTag.Version+1)
	}

	target, _, err := repo.CreateTicket(ctx, CreateInput{
		OrgID: orgA, Actor: Actor{Type: "system"},
		RequesterEmail: "req@test.local", RequesterName: "Req",
		Subject: "merge target", Body: "b", Priority: "normal",
	})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	_, mergedTarget, err := repo.MergeTickets(ctx, orgA, number, target.Number, actor)
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if mergedTarget.Version != target.Version+1 {
		t.Fatalf("merge target version = %d, want %d", mergedTarget.Version, target.Version+1)
	}

	// The merge appends a system comment to the target, so the target must be
	// reindexed through its own internal event even though the source carries
	// the customer-visible ticket.merged event.
	var targetEvents int
	if err := repo.pool.QueryRow(ctx,
		`SELECT count(*) FROM ticket_events
		 WHERE ticket_id = $1 AND event_type = 'ticket.merge_target_updated'`,
		target.ID,
	).Scan(&targetEvents); err != nil {
		t.Fatalf("count target events: %v", err)
	}
	if targetEvents != 1 {
		t.Fatalf("merge target events = %d, want 1", targetEvents)
	}
}
