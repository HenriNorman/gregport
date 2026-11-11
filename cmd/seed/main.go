// Command seed generates realistic tickets, comments, and tags in Postgres for
// load testing and latency benchmarking.
//
// It creates its own organisations (with unique slugs, so repeated runs never
// collide), customers, agents, tags, tickets, comments, and ticket tags. Rows
// are written with COPY, which is the fastest bulk path into Postgres.
//
// Usage:
//
//	go run ./cmd/seed
//
// Environment:
//
//	SEED_TICKETS          total tickets to create (default 1000000)
//	SEED_ORGS             organisations to spread them across (default 5)
//	SEED_CUSTOMERS        customers per organisation (default 1000)
//	SEED_USERS            agent users to create globally (default 50)
//	SEED_TAGS             tags per organisation (default 12)
//	SEED_BATCH            tickets per batch (default 5000)
//	SEED_RANDOM_SEED      integer RNG seed (default time-based)
//
// The seeded data deliberately covers the full ticket lifecycle: statuses,
// priorities, channels, assignees, tags, and 0-5 comments per ticket. This
// makes Postgres full-text search and Elasticsearch latency numbers comparable
// on a realistic corpus rather than an empty table.
package main

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"math/rand"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	"github.com/HenriNorman/gregport/internal/platform/config"
	"github.com/HenriNorman/gregport/internal/platform/database"
	"github.com/HenriNorman/gregport/internal/platform/logging"
)

var (
	subjects = []string{
		"Printer not working",
		"Cannot log in to account",
		"Email not sending or receiving",
		"Website is down",
		"Billing question about last invoice",
		"Feature request: dark mode",
		"Bug: button does not submit form",
		"Password reset not arriving",
		"API returning 500 errors",
		"Application running slowly",
		"Cannot upload attachment",
		"Integration webhook not firing",
		"Mobile app crashes on startup",
		"Two-factor authentication broken",
		"Data export missing records",
		"Duplicate charge on credit card",
		"Search returns no results",
		"Notification emails are delayed",
		"SAML single sign-on failing",
		"CSV import rejects valid rows",
		"Timezone is wrong on timestamps",
		"Dashboard charts do not load",
		"Refund request for cancelled plan",
		"Access denied after role change",
	}

	// Bodies are templates; {subject} is replaced with the ticket subject so
	// comments share vocabulary with their ticket. Search queries that match a
	// subject therefore also match comment text, which is the realistic case.
	bodies = []string{
		"I am experiencing an issue with {subject}. This started happening yesterday and is blocking my work. Can you please help?",
		"When I try to reproduce {subject}, I get an error message. This is urgent as our team cannot proceed without it.",
		"I noticed that {subject} is not behaving as expected. Steps to reproduce: open the app, navigate to settings, run the action.",
		"Is there a way to work around {subject}? I looked through the documentation but could not find instructions.",
		"We need {subject} fixed as soon as possible. Our customers are affected and we are receiving complaints.",
		"I would like to request an improvement related to {subject}. It would speed up our workflow significantly.",
		"Since the latest update, {subject} has stopped working. Please investigate and provide a fix.",
		"Can someone look into {subject}? It has been reported by multiple users in our organisation.",
		"I am getting unexpected results when I test {subject}. The behaviour seems inconsistent across environments.",
		"Please confirm whether {subject} is a known issue. We are planning our release and need to know if it will be resolved.",
		"Thanks for the update. {subject} is still happening intermittently, roughly every other attempt.",
		"I attached a screenshot and the request id for {subject}. Let me know if you need more details.",
		"Following up on {subject}: the workaround helps, but the underlying problem is still there.",
		"We upgraded to the latest version and {subject} broke again. Rolling back is not an option for us.",
		"Could you escalate {subject}? It is now affecting our production traffic and our on-call rotation.",
	}

	firstNames = []string{
		"John", "Jane", "Robert", "Maria", "David", "Susan", "Michael", "Lisa",
		"James", "Jennifer", "Richard", "Patricia", "Thomas", "Linda", "Charles",
		"Elizabeth", "Christopher", "Barbara", "Daniel", "Sarah", "Ahmed", "Fatima",
		"Wei", "Priya", "Diego", "Sofia", "Kwame", "Aisha", "Yuki", "Noah",
	}

	lastNames = []string{
		"Smith", "Johnson", "Williams", "Brown", "Jones", "Garcia", "Miller",
		"Davis", "Rodriguez", "Martinez", "Hernandez", "Lopez", "Gonzalez",
		"Wilson", "Anderson", "Thomas", "Taylor", "Moore", "Jackson", "Martin",
		"Okafor", "Kim", "Patel", "Nguyen", "Silva", "Rossi", "Haddad", "Novak",
	}

	// ticket_status is an enum: new, open, pending, solved, closed.
	statuses = []string{
		"new", "new", "new",
		"open", "open", "open", "open",
		"pending", "pending",
		"solved", "solved", "solved",
		"closed", "closed",
	}

	priorities = []string{
		"low", "low",
		"normal", "normal", "normal", "normal",
		"high", "high", "high",
		"urgent",
	}

	// ticket_channel is an enum: api, email, widget, web.
	channels = []string{"api", "email", "email", "widget", "web"}

	tagNames = []string{
		"billing", "login", "crash", "performance", "feature-request",
		"bug", "urgent", "refund", "api", "mobile", "email", "onboarding",
	}
)

func main() {
	if err := godotenv.Load(); err != nil {
		log.Println("no .env file, relying on real env vars")
	}
	cfg, err := config.LoadWorker()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	logger, err := logging.New(cfg.LogLevel)
	if err != nil {
		log.Fatalf("logging: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	seed := envInt64("SEED_RANDOM_SEED", time.Now().UnixNano())

	pool, err := database.NewPool(ctx, cfg.DSN())
	if err != nil {
		logger.Error("database unavailable", "error", err)
		os.Exit(1)
	}
	defer pool.Close()
	logger.Info("connected to postgres")

	s := &Seeder{
		pool:      pool,
		logger:    logger,
		rng:       rand.New(rand.NewSource(seed)),
		tickets:   envInt("SEED_TICKETS", 1_000_000),
		orgs:      envInt("SEED_ORGS", 5),
		customers: envInt("SEED_CUSTOMERS", 1000),
		users:     envInt("SEED_USERS", 50),
		tags:      envInt("SEED_TAGS", len(tagNames)),
		batch:     envInt("SEED_BATCH", 5000),
	}
	if err := s.validate(); err != nil {
		logger.Error("invalid seed configuration", "error", err)
		os.Exit(1)
	}

	started := time.Now()
	if err := s.Run(ctx); err != nil {
		logger.Error("seed failed", "error", err)
		os.Exit(1)
	}
	logger.Info("seed finished",
		"tickets", s.ticketsInserted,
		"comments", s.commentsInserted,
		"ticket_tags", s.ticketTagsInserted,
		"duration", time.Since(started).Round(time.Second).String(),
	)
}

// Seeder writes the synthetic dataset.
type Seeder struct {
	pool   *pgxpool.Pool
	logger *slog.Logger
	rng    *rand.Rand

	tickets   int
	orgs      int
	customers int
	users     int
	tags      int
	batch     int

	ticketsInserted    int
	commentsInserted   int
	ticketTagsInserted int
}

func (s *Seeder) validate() error {
	switch {
	case s.tickets < 1:
		return fmt.Errorf("SEED_TICKETS must be positive")
	case s.orgs < 1:
		return fmt.Errorf("SEED_ORGS must be positive")
	case s.customers < 1:
		return fmt.Errorf("SEED_CUSTOMERS must be positive")
	case s.users < 1:
		return fmt.Errorf("SEED_USERS must be positive")
	case s.tags < 1 || s.tags > len(tagNames):
		return fmt.Errorf("SEED_TAGS must be between 1 and %d", len(tagNames))
	case s.batch < 1:
		return fmt.Errorf("SEED_BATCH must be positive")
	}
	return nil
}

// Run seeds all organisations.
func (s *Seeder) Run(ctx context.Context) error {
	users, err := s.createUsers(ctx, s.users)
	if err != nil {
		return fmt.Errorf("create users: %w", err)
	}

	perOrg := s.tickets / s.orgs
	remainder := s.tickets % s.orgs
	runID := time.Now().UTC().Format("20060102t150405")

	for org := 0; org < s.orgs; org++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		count := perOrg
		if org < remainder {
			count++
		}

		orgID, err := s.createOrganisation(ctx, org, runID)
		if err != nil {
			return fmt.Errorf("create organisation %d: %w", org, err)
		}

		customers, err := s.createCustomers(ctx, orgID, s.customers)
		if err != nil {
			return fmt.Errorf("create customers: %w", err)
		}

		tags, err := s.createTags(ctx, orgID)
		if err != nil {
			return fmt.Errorf("create tags: %w", err)
		}

		if err := s.seedTickets(ctx, orgID, customers, users, tags, count); err != nil {
			return fmt.Errorf("seed tickets for org %d: %w", org, err)
		}

		s.logger.Info("organisation seeded",
			"org", org+1, "of", s.orgs, "org_id", orgID,
			"tickets", count, "total", s.ticketsInserted, "of", s.tickets)
	}

	return nil
}

func (s *Seeder) createOrganisation(ctx context.Context, idx int, runID string) (uuid.UUID, error) {
	name := fmt.Sprintf("Seed Organisation %d (%s)", idx+1, runID)
	slug := fmt.Sprintf("seed-%s-%d", runID, idx+1)

	var id uuid.UUID
	err := s.pool.QueryRow(ctx,
		`INSERT INTO organizations (name, slug, plan, ticket_counter)
		 VALUES ($1, $2, 'free', 0)
		 RETURNING id`,
		name, slug,
	).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("insert org: %w", err)
	}
	return id, nil
}

func (s *Seeder) createCustomers(ctx context.Context, orgID uuid.UUID, n int) ([]uuid.UUID, error) {
	rows := make([][]any, 0, n)
	ids := make([]uuid.UUID, 0, n)
	suffix := orgID.String()[:8]

	for i := 0; i < n; i++ {
		id := uuid.New()
		email := fmt.Sprintf("customer-%d-%s@%s.seed.test", i, shortID(), suffix)
		name := s.randFirstName() + " " + s.randLastName()
		rows = append(rows, []any{id, orgID, email, name})
		ids = append(ids, id)
	}

	_, err := s.pool.CopyFrom(ctx,
		pgx.Identifier{"customers"},
		[]string{"id", "org_id", "email", "name"},
		pgx.CopyFromRows(rows),
	)
	if err != nil {
		return nil, fmt.Errorf("copy customers: %w", err)
	}
	return ids, nil
}

func (s *Seeder) createUsers(ctx context.Context, n int) ([]uuid.UUID, error) {
	rows := make([][]any, 0, n)
	ids := make([]uuid.UUID, 0, n)

	for i := 0; i < n; i++ {
		id := uuid.New()
		email := fmt.Sprintf("agent-%d-%s@seed.test", i, shortID())
		name := s.randFirstName() + " " + s.randLastName()
		rows = append(rows, []any{
			id, email, "seed-password-hash", name, time.Now().UTC(),
		})
		ids = append(ids, id)
	}

	_, err := s.pool.CopyFrom(ctx,
		pgx.Identifier{"users"},
		[]string{"id", "email", "password_hash", "name", "email_verified_at"},
		pgx.CopyFromRows(rows),
	)
	if err != nil {
		return nil, fmt.Errorf("copy users: %w", err)
	}
	return ids, nil
}

func (s *Seeder) createTags(ctx context.Context, orgID uuid.UUID) ([]uuid.UUID, error) {
	names := append([]string(nil), tagNames[:s.tags]...)
	s.rng.Shuffle(len(names), func(i, j int) { names[i], names[j] = names[j], names[i] })

	rows := make([][]any, 0, len(names))
	ids := make([]uuid.UUID, 0, len(names))
	for _, name := range names {
		id := uuid.New()
		rows = append(rows, []any{id, orgID, name})
		ids = append(ids, id)
	}

	_, err := s.pool.CopyFrom(ctx,
		pgx.Identifier{"tags"},
		[]string{"id", "org_id", "name"},
		pgx.CopyFromRows(rows),
	)
	if err != nil {
		return nil, fmt.Errorf("copy tags: %w", err)
	}
	return ids, nil
}

type ticketMeta struct {
	id          uuid.UUID
	createdAt   time.Time
	requesterID uuid.UUID
	subject     string
}

func (s *Seeder) seedTickets(
	ctx context.Context,
	orgID uuid.UUID,
	customers, users, tags []uuid.UUID,
	n int,
) error {
	now := time.Now().UTC()
	// Spread tickets over two years so ordering, cursors, and date filters are
	// exercised on a realistic distribution.
	oldest := now.Add(-2 * 365 * 24 * time.Hour)

	number := int64(0)

	for offset := 0; offset < n; offset += s.batch {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		batchSize := n - offset
		if batchSize > s.batch {
			batchSize = s.batch
		}

		tx, err := s.pool.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin batch: %w", err)
		}

		metas, ticketRows := s.buildTickets(orgID, customers, users, number, batchSize, oldest, now)
		number += int64(batchSize)

		if _, err := tx.CopyFrom(ctx,
			pgx.Identifier{"tickets"},
			[]string{
				"id", "org_id", "number", "requester_id", "assignee_id", "team_id",
				"sla_policy_id", "merged_into_id", "subject", "status", "priority",
				"channel", "first_response_due_at", "resolution_due_at",
				"first_response_at", "resolved_at", "closed_at", "created_at",
				"updated_at", "deleted_at", "version",
			},
			pgx.CopyFromRows(ticketRows),
		); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("copy tickets: %w", err)
		}

		commentRows := s.buildComments(orgID, metas, users, now)
		if len(commentRows) > 0 {
			if _, err := tx.CopyFrom(ctx,
				pgx.Identifier{"comments"},
				[]string{
					"id", "org_id", "ticket_id", "author_type", "author_user_id",
					"author_customer_id", "body", "is_internal", "created_at",
					"deleted_at",
				},
				pgx.CopyFromRows(commentRows),
			); err != nil {
				_ = tx.Rollback(ctx)
				return fmt.Errorf("copy comments: %w", err)
			}
		}

		tagRows := s.buildTicketTags(metas, tags)
		if len(tagRows) > 0 {
			if _, err := tx.CopyFrom(ctx,
				pgx.Identifier{"ticket_tags"},
				[]string{"ticket_id", "tag_id"},
				pgx.CopyFromRows(tagRows),
			); err != nil {
				_ = tx.Rollback(ctx)
				return fmt.Errorf("copy ticket tags: %w", err)
			}
		}

		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit batch: %w", err)
		}

		s.ticketsInserted += batchSize
		s.commentsInserted += len(commentRows)
		s.ticketTagsInserted += len(tagRows)

		if s.ticketsInserted%(s.batch*20) == 0 {
			s.logger.Info("seeding progress",
				"tickets", s.ticketsInserted, "of", n,
				"comments", s.commentsInserted,
				"ticket_tags", s.ticketTagsInserted)
		}
	}

	if _, err := s.pool.Exec(ctx,
		`UPDATE organizations SET ticket_counter = $1, updated_at = now() WHERE id = $2`,
		number, orgID,
	); err != nil {
		return fmt.Errorf("update ticket counter: %w", err)
	}
	return nil
}

func (s *Seeder) buildTickets(
	orgID uuid.UUID,
	customers, users []uuid.UUID,
	firstNumber int64,
	count int,
	oldest, now time.Time,
) ([]ticketMeta, [][]any) {
	metas := make([]ticketMeta, 0, count)
	rows := make([][]any, 0, count)

	for i := 0; i < count; i++ {
		id := uuid.New()
		requester := s.randElement(customers)
		createdAt := s.randomTime(oldest, now)
		updatedAt := createdAt
		if s.rng.Intn(4) != 0 {
			maxUpdate := now
			if createdCap := createdAt.Add(30 * 24 * time.Hour); createdCap.Before(now) {
				maxUpdate = createdCap
			}
			updatedAt = s.randomTime(createdAt, maxUpdate)
		}

		subject := s.randElement(subjects)
		status := s.randElement(statuses)
		priority := s.randElement(priorities)
		channel := s.randElement(channels)

		var assignee any
		if s.rng.Intn(5) != 0 {
			assignee = s.randElement(users)
		}

		var firstResponse, resolved, closed any
		if status != "new" && s.rng.Intn(3) != 0 {
			firstResponse = s.randomTime(createdAt, updatedAt)
		}
		switch status {
		case "solved":
			resolved = s.randomTime(createdAt, updatedAt)
		case "closed":
			resolved = s.randomTime(createdAt, updatedAt)
			closed = s.randomTime(resolved.(time.Time), updatedAt)
		}

		// Due dates: response SLA within 8h, resolution SLA within 72h of
		// creation. They are intentionally before the first response for some
		// tickets so breach logic has data to work with.
		firstResponseDue := createdAt.Add(time.Duration(1+s.rng.Intn(8)) * time.Hour)
		resolutionDue := createdAt.Add(time.Duration(8+s.rng.Intn(64)) * time.Hour)

		rows = append(rows, []any{
			id, orgID, firstNumber + int64(i) + 1, requester, assignee,
			nil, nil, nil,
			subject, status, priority, channel,
			firstResponseDue, resolutionDue,
			firstResponse, resolved, closed,
			createdAt, updatedAt, nil, 1,
		})
		metas = append(metas, ticketMeta{
			id: id, createdAt: createdAt, requesterID: requester, subject: subject,
		})
	}

	return metas, rows
}

func (s *Seeder) buildComments(
	orgID uuid.UUID,
	metas []ticketMeta,
	users []uuid.UUID,
	now time.Time,
) [][]any {
	var rows [][]any

	for _, meta := range metas {
		// 0-5 comments, weighted toward 1-3.
		n := s.rng.Intn(6)
		lastAt := meta.createdAt

		for j := 0; j < n; j++ {
			at := s.randomTime(lastAt, now)
			lastAt = at

			// 65% customer, 30% agent, 5% system.
			roll := s.rng.Intn(100)
			authorType := "customer"
			var authorUserID, authorCustomerID any
			switch {
			case roll < 65:
				authorCustomerID = meta.requesterID
			case roll < 95:
				authorType = "user"
				authorUserID = s.randElement(users)
			default:
				authorType = "system"
			}

			// Internal notes only come from agents, as in the real API.
			isInternal := authorType == "user" && s.rng.Intn(10) == 0

			rows = append(rows, []any{
				uuid.New(), orgID, meta.id, authorType,
				authorUserID, authorCustomerID,
				s.commentBody(meta.subject), isInternal, at, nil,
			})
		}
	}

	return rows
}

func (s *Seeder) buildTicketTags(metas []ticketMeta, tags []uuid.UUID) [][]any {
	var rows [][]any

	for _, meta := range metas {
		// 0-2 distinct tags per ticket.
		n := s.rng.Intn(3)
		if n == 0 {
			continue
		}
		chosen := make(map[uuid.UUID]struct{}, n)
		for attempt := 0; attempt < n*4 && len(chosen) < n; attempt++ {
			chosen[s.randElement(tags)] = struct{}{}
		}
		for tagID := range chosen {
			rows = append(rows, []any{meta.id, tagID})
		}
	}

	return rows
}

func (s *Seeder) commentBody(subject string) string {
	return strings.ReplaceAll(s.randElement(bodies), "{subject}", strings.ToLower(subject))
}

func (s *Seeder) randElement[T any](slice []T) T {
	return slice[s.rng.Intn(len(slice))]
}

func (s *Seeder) randFirstName() string {
	return s.randElement(firstNames)
}

func (s *Seeder) randLastName() string {
	return s.randElement(lastNames)
}

// randomTime returns a random time in [from, to]. It tolerates from == to.
func (s *Seeder) randomTime(from, to time.Time) time.Time {
	if !to.After(from) {
		return from
	}
	delta := to.UnixNano() - from.UnixNano()
	return from.Add(time.Duration(s.rng.Int63n(delta)))
}

func envInt(name string, fallback int) int {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	v, err := strconv.Atoi(raw)
	if err != nil {
		log.Fatalf("%s: %v", name, err)
	}
	return v
}

func envInt64(name string, fallback int64) int64 {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		log.Fatalf("%s: %v", name, err)
	}
	return v
}

func shortID() string {
	return uuid.NewString()[:8]
}
