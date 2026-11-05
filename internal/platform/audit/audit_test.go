package audit

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, "postgres://postgres:Henri123@localhost:5432/gregport?sslmode=disable")
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestRecord_InsertsRow(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	var orgID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO organizations (name, slug) VALUES ('Audit Test', $1) RETURNING id`,
		"audit-"+uuid.NewString()[:8],
	).Scan(&orgID); err != nil {
		t.Fatalf("org: %v", err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM audit_logs WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM organizations WHERE id = $1`, orgID)
	})

	entityID := uuid.New()
	actorID := uuid.New()
	actor := requestctx.Principal{
		Kind:    requestctx.KindUser,
		ActorID: actorID,
		OrgID:   orgID,
		Role:    "owner",
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := Record(ctx, tx, orgID, actor, "test.action", "test_entity", entityID, map[string]any{"k": "v"}); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("record: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var (
		actorType string
		gotActor  *uuid.UUID
		action    string
		entType   string
		gotEnt    *uuid.UUID
	)
	if err := pool.QueryRow(ctx,
		`SELECT actor_type, actor_id, action, entity_type, entity_id
		 FROM audit_logs WHERE org_id = $1`, orgID,
	).Scan(&actorType, &gotActor, &action, &entType, &gotEnt); err != nil {
		t.Fatalf("query: %v", err)
	}
	if actorType != "user" {
		t.Fatalf("expected actor_type user, got %s", actorType)
	}
	if gotActor == nil || *gotActor != actorID {
		t.Fatalf("actor_id mismatch: %v", gotActor)
	}
	if action != "test.action" || entType != "test_entity" {
		t.Fatalf("action/entity mismatch: %s / %s", action, entType)
	}
	if gotEnt == nil || *gotEnt != entityID {
		t.Fatalf("entity_id mismatch: %v", gotEnt)
	}
}

func TestPurgeOldLogs(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	var orgID uuid.UUID
	if err := pool.QueryRow(ctx,
		`INSERT INTO organizations (name, slug) VALUES ('Audit Purge Test', $1) RETURNING id`,
		"auditpurge-"+uuid.NewString()[:8],
	).Scan(&orgID); err != nil {
		t.Fatalf("org: %v", err)
	}
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.Exec(bg, `DELETE FROM audit_logs WHERE org_id = $1`, orgID)
		_, _ = pool.Exec(bg, `DELETE FROM organizations WHERE id = $1`, orgID)
	})

	old := time.Now().Add(-48 * time.Hour)
	recent := time.Now().Add(-1 * time.Hour)

	insert := func(createdAt time.Time) {
		if _, err := pool.Exec(ctx,
			`INSERT INTO audit_logs (org_id, actor_type, actor_id, action, entity_type, entity_id, data, created_at)
			 VALUES ($1, 'system', NULL, 'test.action', 'test_entity', NULL, '{}', $2)`,
			orgID, createdAt,
		); err != nil {
			t.Fatalf("insert audit log: %v", err)
		}
	}
	insert(old)
	insert(old)
	insert(recent)

	n, err := PurgeOldLogs(ctx, pool, old, 100)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if n != 2 {
		t.Fatalf("purged: %d, want 2 (old audit logs)", n)
	}

	var count int
	if err := pool.QueryRow(ctx,
		`SELECT count(*) FROM audit_logs WHERE org_id = $1`, orgID,
	).Scan(&count); err != nil {
		t.Fatalf("count audit logs: %v", err)
	}
	if count != 1 {
		t.Fatalf("remaining audit logs: %d, want 1 (recent)", count)
	}
}
