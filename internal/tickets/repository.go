package tickets

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/HenriNorman/gregport/internal/platform/database"
)

type ListFilter struct {
	Status      *string
	Priority    *string
	AssigneeID  *uuid.UUID
	RequesterID *uuid.UUID
	Limit       int
	Cursor      string
}

type ListResult struct {
	Tickets    []Ticket
	NextCursor string
}

type Repository struct {
	pool *pgxpool.Pool
}

type UpdateInput struct {
	OrgID           uuid.UUID
	Number          int64
	ExpectedVersion int
	Actor           Actor
	Status          *string
	Priority        *string
	Subject         *string
}

func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

type CreateInput struct {
	OrgID          uuid.UUID
	Actor          Actor
	RequesterEmail string
	RequesterName  string
	Subject        string
	Body           string
	Priority       string
}

type AssignInput struct {
	OrgID      uuid.UUID
	Number     int64
	Actor      Actor
	AssigneeID *uuid.UUID
	TeamID     *uuid.UUID
}

func (r *Repository) CreateTicket(ctx context.Context, in CreateInput) (Ticket, Comment, error) {
	var (
		ticket  Ticket
		comment Comment
	)

	err := database.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		var number int64
		if err := tx.QueryRow(ctx,
			`UPDATE organizations
			 SET ticket_counter = ticket_counter + 1, updated_at = now()
			 WHERE id = $1
			 RETURNING ticket_counter`,
			in.OrgID,
		).Scan(&number); err != nil {
			return fmt.Errorf("increment counter: %w", err)
		}

		var requesterID uuid.UUID
		var requesterName *string
		if in.RequesterName != "" {
			requesterName = &in.RequesterName
		}
		if err := tx.QueryRow(ctx,
			`INSERT INTO customers (org_id, email, name)
			 VALUES ($1, $2, $3)
			 ON CONFLICT (org_id, email)
			 DO UPDATE SET name = COALESCE(EXCLUDED.name, customers.name), updated_at = now()
			 RETURNING id`,
			in.OrgID, in.RequesterEmail, requesterName,
		).Scan(&requesterID); err != nil {
			return fmt.Errorf("upsert customer: %w", err)
		}

		if err := tx.QueryRow(ctx,
			`INSERT INTO tickets (org_id, number, requester_id, subject, priority, channel)
			 VALUES ($1, $2, $3, $4, $5, 'api')
			 RETURNING id, org_id, number, requester_id, subject, status, priority,
			           channel, version, created_at, updated_at`,
			in.OrgID, number, requesterID, in.Subject, in.Priority,
		).Scan(
			&ticket.ID, &ticket.OrgID, &ticket.Number, &ticket.RequesterID,
			&ticket.Subject, &ticket.Status, &ticket.Priority, &ticket.Channel,
			&ticket.Version, &ticket.CreatedAt, &ticket.UpdatedAt,
		); err != nil {
			return fmt.Errorf("insert ticket: %w", err)
		}

		ticket.RequesterEmail = in.RequesterEmail
		ticket.RequesterName = in.RequesterName

		if err := tx.QueryRow(ctx,
			`INSERT INTO comments (org_id, ticket_id, author_type, author_customer_id, body, is_internal)
			 VALUES ($1, $2, 'customer', $3, $4, false)
			 RETURNING id, org_id, ticket_id, author_type, author_user_id,
			           author_customer_id, body, is_internal, created_at`,
			in.OrgID, ticket.ID, requesterID, in.Body,
		).Scan(
			&comment.ID, &comment.OrgID, &comment.TicketID, &comment.AuthorType,
			&comment.AuthorUserID, &comment.AuthorCustomerID, &comment.Body,
			&comment.IsInternal, &comment.CreatedAt,
		); err != nil {
			return fmt.Errorf("insert first comment: %w", err)
		}

		if err := Record(ctx, tx, in.OrgID, ticket.ID, in.Actor, "ticket.created", map[string]any{
			"number":   ticket.Number,
			"subject":  ticket.Subject,
			"priority": ticket.Priority,
		}); err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		return Ticket{}, Comment{}, err
	}
	return ticket, comment, nil
}

func (r *Repository) ListTickets(ctx context.Context, orgID uuid.UUID, f ListFilter) (ListResult, error) {
	if f.Limit <= 0 {
		f.Limit = 25
	}
	if f.Limit > 100 {
		f.Limit = 100
	}

	cur, err := decodeCursor(f.Cursor)
	if err != nil {
		return ListResult{}, err
	}

	args := []any{orgID}
	where := "WHERE t.org_id = $1 AND t.deleted_at IS NULL"
	add := func(clause string, val any) {
		args = append(args, val)
		where += fmt.Sprintf(" AND %s = $%d", clause, len(args))
	}

	if f.Status != nil {
		add("t.status", *f.Status)
	}
	if f.Priority != nil {
		add("t.priority", *f.Priority)
	}
	if f.AssigneeID != nil {
		add("t.assignee_id", *f.AssigneeID)
	}
	if f.RequesterID != nil {
		add("t.requester_id", *f.RequesterID)
	}
	if !cur.CreatedAt.IsZero() {
		args = append(args, cur.CreatedAt, cur.ID)
		where += fmt.Sprintf(" AND (t.created_at, t.id) < ($%d, $%d)", len(args)-1, len(args))
	}

	args = append(args, f.Limit+1)
	query := fmt.Sprintf(
		`SELECT t.id, t.org_id, t.number, t.requester_id, t.assignee_id, t.team_id,
		        t.sla_policy_id, t.merged_into_id, t.subject, t.status, t.priority,
		        t.channel, t.first_response_due_at, t.resolution_due_at,
		        t.first_response_at, t.resolved_at, t.closed_at, t.version,
		        t.created_at, t.updated_at, t.deleted_at,
		        c.email, COALESCE(c.name, '')
		 FROM tickets t
		 JOIN customers c ON c.id = t.requester_id
		 %s
		 ORDER BY t.created_at DESC, t.id DESC
		 LIMIT $%d`,
		where, len(args),
	)

	rows, err := r.pool.Query(ctx, query, args...)
	if err != nil {
		return ListResult{}, fmt.Errorf("list tickets: %w", err)
	}
	defer rows.Close()

	var out []Ticket
	for rows.Next() {
		var t Ticket
		if err := rows.Scan(
			&t.ID, &t.OrgID, &t.Number, &t.RequesterID, &t.AssigneeID, &t.TeamID,
			&t.SLAPolicyID, &t.MergedIntoID, &t.Subject, &t.Status, &t.Priority,
			&t.Channel, &t.FirstResponseDueAt, &t.ResolutionDueAt, &t.FirstResponseAt,
			&t.ResolvedAt, &t.ClosedAt, &t.Version, &t.CreatedAt, &t.UpdatedAt, &t.DeletedAt,
			&t.RequesterEmail, &t.RequesterName,
		); err != nil {
			return ListResult{}, fmt.Errorf("scan ticket: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return ListResult{}, fmt.Errorf("rows: %w", err)
	}

	var next string
	if len(out) > f.Limit {
		out = out[:f.Limit]
		last := out[len(out)-1]
		next = encodeCursor(last.CreatedAt, last.ID)
	}

	return ListResult{Tickets: out, NextCursor: next}, nil
}

func (r *Repository) GetTicket(ctx context.Context, orgID uuid.UUID, number int64) (Ticket, error) {
	var t Ticket
	err := r.pool.QueryRow(ctx,
		`SELECT t.id, t.org_id, t.number, t.requester_id, t.assignee_id, t.team_id,
		        t.sla_policy_id, t.merged_into_id, t.subject, t.status, t.priority,
		        t.channel, t.first_response_due_at, t.resolution_due_at,
		        t.first_response_at, t.resolved_at, t.closed_at, t.version,
		        t.created_at, t.updated_at, t.deleted_at,
		        c.email, COALESCE(c.name, '')
		 FROM tickets t
		 JOIN customers c ON c.id = t.requester_id
		 WHERE t.org_id = $1 AND t.number = $2 AND t.deleted_at IS NULL`,
		orgID, number,
	).Scan(
		&t.ID, &t.OrgID, &t.Number, &t.RequesterID, &t.AssigneeID, &t.TeamID,
		&t.SLAPolicyID, &t.MergedIntoID, &t.Subject, &t.Status, &t.Priority,
		&t.Channel, &t.FirstResponseDueAt, &t.ResolutionDueAt, &t.FirstResponseAt,
		&t.ResolvedAt, &t.ClosedAt, &t.Version, &t.CreatedAt, &t.UpdatedAt, &t.DeletedAt,
		&t.RequesterEmail, &t.RequesterName,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Ticket{}, ErrTicketNotFound
		}
		return Ticket{}, fmt.Errorf("get ticket: %w", err)
	}
	return t, nil
}

func (r *Repository) UpdateTicket(ctx context.Context, in UpdateInput) (Ticket, error) {
	var out Ticket

	err := database.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		var (
			curID       uuid.UUID
			curStatus   string
			curVersion  int
			curResolved *time.Time
		)
		if err := tx.QueryRow(ctx,
			`SELECT id, status, version, resolved_at
			 FROM tickets
			 WHERE org_id = $1 AND number = $2 AND deleted_at IS NULL
			 FOR UPDATE`,
			in.OrgID, in.Number,
		).Scan(&curID, &curStatus, &curVersion, &curResolved); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrTicketNotFound
			}
			return fmt.Errorf("lock ticket: %w", err)
		}

		if curVersion != in.ExpectedVersion {
			return ErrVersionMismatch
		}

		newStatus := curStatus
		if in.Status != nil && *in.Status != curStatus {
			if !CanTransition(curStatus, *in.Status) {
				return ErrInvalidTransition
			}
			newStatus = *in.Status
		}

		args := []any{}
		sets := []string{"version = version + 1", "updated_at = now()"}
		add := func(col string, val any) {
			args = append(args, val)
			sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
		}

		if in.Status != nil && *in.Status != curStatus {
			add("status", *in.Status)
			switch *in.Status {
			case "open":
				if curStatus == "solved" {
					sets = append(sets, "resolved_at = NULL")
				} else {
					sets = append(sets, "first_response_at = COALESCE(first_response_at, now())")
				}
			case "solved":
				sets = append(sets, "resolved_at = now()")
			case "closed":
				sets = append(sets, "closed_at = now()")
			}
		}
		if in.Priority != nil {
			add("priority", *in.Priority)
		}
		if in.Subject != nil {
			add("subject", *in.Subject)
		}

		args = append(args, in.OrgID, in.Number, in.ExpectedVersion)
		whereOrg := len(args) - 2
		whereNum := len(args) - 1
		whereVer := len(args)

		query := fmt.Sprintf(
			`UPDATE tickets SET %s
			 WHERE org_id = $%d AND number = $%d AND version = $%d
			 RETURNING id, org_id, number, requester_id, assignee_id, team_id,
			           sla_policy_id, merged_into_id, subject, status, priority,
			           channel, first_response_due_at, resolution_due_at,
			           first_response_at, resolved_at, closed_at, version,
			           created_at, updated_at, deleted_at`,
			strings.Join(sets, ", "), whereOrg, whereNum, whereVer,
		)

		if err := tx.QueryRow(ctx, query, args...).Scan(
			&out.ID, &out.OrgID, &out.Number, &out.RequesterID, &out.AssigneeID,
			&out.TeamID, &out.SLAPolicyID, &out.MergedIntoID, &out.Subject,
			&out.Status, &out.Priority, &out.Channel, &out.FirstResponseDueAt,
			&out.ResolutionDueAt, &out.FirstResponseAt, &out.ResolvedAt,
			&out.ClosedAt, &out.Version, &out.CreatedAt, &out.UpdatedAt, &out.DeletedAt,
		); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrVersionMismatch
			}
			return fmt.Errorf("update ticket: %w", err)
		}

		if newStatus != curStatus {
			if err := Record(ctx, tx, in.OrgID, out.ID, in.Actor, "ticket.status_changed", map[string]any{
				"old": curStatus,
				"new": newStatus,
			}); err != nil {
				return err
			}
		}

		if err := tx.QueryRow(ctx,
			`SELECT c.email, COALESCE(c.name, '') FROM customers c WHERE c.id = $1`,
			out.RequesterID,
		).Scan(&out.RequesterEmail, &out.RequesterName); err != nil {
			return fmt.Errorf("load requester: %w", err)
		}

		return nil
	})
	if err != nil {
		return Ticket{}, err
	}
	return out, nil
}

type CreateCommentInput struct {
	OrgID      uuid.UUID
	Number     int64
	Actor      Actor
	Body       string
	IsInternal bool
}

func (r *Repository) SoftDeleteTicket(ctx context.Context, orgID uuid.UUID, number int64, actor Actor) error {
	return database.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		var ticketID uuid.UUID
		if err := tx.QueryRow(ctx,
			`UPDATE tickets
			 SET deleted_at = now(), version = version + 1, updated_at = now()
			 WHERE org_id = $1 AND number = $2 AND deleted_at IS NULL
			 RETURNING id`,
			orgID, number,
		).Scan(&ticketID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrTicketNotFound
			}
			return fmt.Errorf("soft delete ticket: %w", err)
		}

		return Record(ctx, tx, orgID, ticketID, actor, "ticket.deleted", nil)
	})
}

func (r *Repository) CreateComment(ctx context.Context, in CreateCommentInput) (Comment, error) {
	var out Comment

	err := database.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		var ticketID uuid.UUID
		if err := tx.QueryRow(ctx,
			`SELECT id FROM tickets
			 WHERE org_id = $1 AND number = $2 AND deleted_at IS NULL`,
			in.OrgID, in.Number,
		).Scan(&ticketID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrTicketNotFound
			}
			return fmt.Errorf("find ticket: %w", err)
		}

		if err := tx.QueryRow(ctx,
			`INSERT INTO comments (org_id, ticket_id, author_type, author_user_id, body, is_internal)
			 VALUES ($1, $2, 'user', $3, $4, $5)
			 RETURNING id, org_id, ticket_id, author_type, author_user_id,
			           author_customer_id, body, is_internal, created_at`,
			in.OrgID, ticketID, in.Actor.ID, in.Body, in.IsInternal,
		).Scan(
			&out.ID, &out.OrgID, &out.TicketID, &out.AuthorType,
			&out.AuthorUserID, &out.AuthorCustomerID, &out.Body,
			&out.IsInternal, &out.CreatedAt,
		); err != nil {
			return fmt.Errorf("insert comment: %w", err)
		}

		// set first_response_at if this is the first public agent comment
		if !in.IsInternal {
			if _, err := tx.Exec(ctx,
				`UPDATE tickets
				 SET updated_at = now(),
				     first_response_at = COALESCE(first_response_at, now())
				 WHERE id = $1`,
				ticketID,
			); err != nil {
				return fmt.Errorf("bump ticket: %w", err)
			}
		} 
		// customer comments have actor_user_id NULL and can't be watchers)
		if in.Actor.ID != nil {
			if _, err := tx.Exec(ctx,
				`INSERT INTO ticket_watchers (ticket_id, user_id) VALUES ($1, $2)
				 ON CONFLICT (ticket_id, user_id) DO NOTHING`,
				ticketID, *in.Actor.ID,
			); err != nil {
				return fmt.Errorf("auto-watch commenter: %w", err)
			}
		} else {
			if _, err := tx.Exec(ctx,
				`UPDATE tickets SET updated_at = now() WHERE id = $1`,
				ticketID,
			); err != nil {
				return fmt.Errorf("bump ticket: %w", err)
			}
		}

		return Record(ctx, tx, in.OrgID, ticketID, in.Actor, "comment.added", map[string]any{
			"comment_id":  out.ID.String(),
			"is_internal": out.IsInternal,
		})
	})
	if err != nil {
		return Comment{}, err
	}
	return out, nil
}

func (r *Repository) ListComments(ctx context.Context, orgID uuid.UUID, number int64, includeInternal bool) ([]Comment, error) {
	query := `SELECT c.id, c.org_id, c.ticket_id, c.author_type, c.author_user_id,
	                 c.author_customer_id, c.body, c.is_internal, c.created_at
	          FROM comments c
	          JOIN tickets t ON t.id = c.ticket_id
	          WHERE t.org_id = $1 AND t.number = $2 AND t.deleted_at IS NULL
	            AND c.deleted_at IS NULL`
	if !includeInternal {
		query += " AND c.is_internal = false"
	}
	query += " ORDER BY c.created_at ASC, c.id ASC"

	rows, err := r.pool.Query(ctx, query, orgID, number)
	if err != nil {
		return nil, fmt.Errorf("list comments: %w", err)
	}
	defer rows.Close()

	var out []Comment
	for rows.Next() {
		var c Comment
		if err := rows.Scan(
			&c.ID, &c.OrgID, &c.TicketID, &c.AuthorType, &c.AuthorUserID,
			&c.AuthorCustomerID, &c.Body, &c.IsInternal, &c.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan comment: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return out, nil
}

func (r *Repository) CreateTeam(ctx context.Context, orgID uuid.UUID, name string) (Team, error) {
	var t Team
	err := r.pool.QueryRow(ctx,
		`INSERT INTO teams (org_id, name) VALUES ($1, $2)
		 RETURNING id, org_id, name, created_at`,
		orgID, name,
	).Scan(&t.ID, &t.OrgID, &t.Name, &t.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Team{}, ErrTeamNameTaken
		}
		return Team{}, fmt.Errorf("create team: %w", err)
	}
	return t, nil
}

func (r *Repository) ListTeams(ctx context.Context, orgID uuid.UUID) ([]Team, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, org_id, name, created_at
		 FROM teams WHERE org_id = $1 ORDER BY name`,
		orgID,
	)
	if err != nil {
		return nil, fmt.Errorf("list teams: %w", err)
	}
	defer rows.Close()

	var out []Team
	for rows.Next() {
		var t Team
		if err := rows.Scan(&t.ID, &t.OrgID, &t.Name, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan team: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return out, nil
}

func (r *Repository) UpdateTeam(ctx context.Context, orgID, teamID uuid.UUID, name string) (Team, error) {
	var t Team
	err := r.pool.QueryRow(ctx,
		`UPDATE teams SET name = $3
		 WHERE org_id = $1 AND id = $2
		 RETURNING id, org_id, name, created_at`,
		orgID, teamID, name,
	).Scan(&t.ID, &t.OrgID, &t.Name, &t.CreatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Team{}, ErrTeamNotFound
		}
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Team{}, ErrTeamNameTaken
		}
		return Team{}, fmt.Errorf("update team: %w", err)
	}
	return t, nil
}

func (r *Repository) DeleteTeam(ctx context.Context, orgID, teamID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM teams WHERE org_id = $1 AND id = $2`,
		orgID, teamID,
	)
	if err != nil {
		return fmt.Errorf("delete team: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrTeamNotFound
	}
	return nil
}

// AddTeamMember verifies org membership and inserts in one transaction.
// The membership check is in the same query as the insert, so a user id
// from the client is never trusted on its own.
func (r *Repository) AddTeamMember(ctx context.Context, orgID, teamID, userID uuid.UUID) error {
	return database.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		var teamExists bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM teams WHERE org_id = $1 AND id = $2)`,
			orgID, teamID,
		).Scan(&teamExists); err != nil {
			return fmt.Errorf("check team: %w", err)
		}
		if !teamExists {
			return ErrTeamNotFound
		}

		var isMember bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (
			   SELECT 1 FROM memberships
			   WHERE org_id = $1 AND user_id = $2
			 )`,
			orgID, userID,
		).Scan(&isMember); err != nil {
			return fmt.Errorf("check membership: %w", err)
		}
		if !isMember {
			return ErrMemberNotInOrg
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO team_members (team_id, user_id)
			 VALUES ($1, $2)
			 ON CONFLICT (team_id, user_id) DO NOTHING`,
			teamID, userID,
		); err != nil {
			return fmt.Errorf("insert team member: %w", err)
		}
		return nil
	})
}

func (r *Repository) RemoveTeamMember(ctx context.Context, orgID, teamID, userID uuid.UUID) error {
	tag, err := r.pool.Exec(ctx,
		`DELETE FROM team_members
		 WHERE team_id = (SELECT id FROM teams WHERE org_id = $1 AND id = $2)
		   AND user_id = $3`,
		orgID, teamID, userID,
	)
	if err != nil {
		return fmt.Errorf("remove team member: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrTeamNotFound
	}
	return nil
}

func (r *Repository) AssignTicket(ctx context.Context, in AssignInput) (Ticket, error) {
	var out Ticket

	err := database.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		var ticketID uuid.UUID
		var oldAssignee *uuid.UUID
		var oldTeam *uuid.UUID
		var curVersion int
		if err := tx.QueryRow(ctx,
			`SELECT id, assignee_id, team_id, version
			 FROM tickets
			 WHERE org_id = $1 AND number = $2 AND deleted_at IS NULL
			 FOR UPDATE`,
			in.OrgID, in.Number,
		).Scan(&ticketID, &oldAssignee, &oldTeam, &curVersion); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrTicketNotFound
			}
			return fmt.Errorf("lock ticket: %w", err)
		}

		if in.AssigneeID != nil {
			var ok bool
			if err := tx.QueryRow(ctx,
				`SELECT EXISTS (
				   SELECT 1 FROM memberships
				   WHERE org_id = $1 AND user_id = $2
				     AND role IN ('owner','admin','agent')
				 )`,
				in.OrgID, *in.AssigneeID,
			).Scan(&ok); err != nil {
				return fmt.Errorf("check assignee: %w", err)
			}
			if !ok {
				return ErrAssigneeNotAgent
			}
		}

		if in.TeamID != nil {
			var ok bool
			if err := tx.QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM teams WHERE org_id = $1 AND id = $2)`,
				in.OrgID, *in.TeamID,
			).Scan(&ok); err != nil {
				return fmt.Errorf("check team: %w", err)
			}
			if !ok {
				return ErrTeamNotFound
			}
		}

		if err := tx.QueryRow(ctx,
			`UPDATE tickets
			 SET assignee_id = COALESCE($3, assignee_id),
			     team_id     = COALESCE($4, team_id),
			     version     = version + 1,
			     updated_at  = now()
			 WHERE org_id = $1 AND number = $2 AND deleted_at IS NULL
			 RETURNING id, org_id, number, requester_id, assignee_id, team_id,
			           sla_policy_id, merged_into_id, subject, status, priority,
			           channel, first_response_due_at, resolution_due_at,
			           first_response_at, resolved_at, closed_at, version,
			           created_at, updated_at, deleted_at`,
			in.OrgID, in.Number, in.AssigneeID, in.TeamID,
		).Scan(
			&out.ID, &out.OrgID, &out.Number, &out.RequesterID, &out.AssigneeID,
			&out.TeamID, &out.SLAPolicyID, &out.MergedIntoID, &out.Subject,
			&out.Status, &out.Priority, &out.Channel, &out.FirstResponseDueAt,
			&out.ResolutionDueAt, &out.FirstResponseAt, &out.ResolvedAt,
			&out.ClosedAt, &out.Version, &out.CreatedAt, &out.UpdatedAt, &out.DeletedAt,
		); err != nil {
			return fmt.Errorf("update ticket: %w", err)
		}

		if err := Record(ctx, tx, in.OrgID, out.ID, in.Actor, "ticket.assigned", map[string]any{
			"old_assignee_id": oldAssignee,
			"new_assignee_id": in.AssigneeID,
			"old_team_id":     oldTeam,
			"new_team_id":     in.TeamID,
		}); err != nil {
			return err
		}

		if in.AssigneeID != nil {
			if _, err := tx.Exec(ctx,
				`INSERT INTO ticket_watchers (ticket_id, user_id)
				 VALUES ($1, $2)
				 ON CONFLICT (ticket_id, user_id) DO NOTHING`,
				out.ID, *in.AssigneeID,
			); err != nil {
				return fmt.Errorf("add watcher: %w", err)
			}
		}

		if err := tx.QueryRow(ctx,
			`SELECT c.email, COALESCE(c.name, '') FROM customers c WHERE c.id = $1`,
			out.RequesterID,
		).Scan(&out.RequesterEmail, &out.RequesterName); err != nil {
			return fmt.Errorf("load requester: %w", err)
		}

		return nil
	})
	if err != nil {
		return Ticket{}, err
	}
	return out, nil
}

// AssignAuto picks the team member with the oldest last_assigned_at and
// assigns the ticket to them, all inside one transaction. SKIP LOCKED on
// team_members means two concurrent auto-assigns pick different agents
// instead of both choosing the same one.
func (r *Repository) AssignAuto(ctx context.Context, in AssignInput) (Ticket, error) {
	var out Ticket

	err := database.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		var ticketID uuid.UUID
		var oldAssignee *uuid.UUID
		var oldTeam *uuid.UUID
		if err := tx.QueryRow(ctx,
			`SELECT id, assignee_id, team_id
			 FROM tickets
			 WHERE org_id = $1 AND number = $2 AND deleted_at IS NULL
			 FOR UPDATE`,
			in.OrgID, in.Number,
		).Scan(&ticketID, &oldAssignee, &oldTeam); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrTicketNotFound
			}
			return fmt.Errorf("lock ticket: %w", err)
		}

		if in.TeamID == nil {
			return ErrInvalidInput
		}

		var picked uuid.UUID
		if err := tx.QueryRow(ctx,
			`SELECT tm.user_id
			 FROM team_members tm
			 JOIN memberships m ON m.user_id = tm.user_id AND m.org_id = $2
			 WHERE tm.team_id = $1
			   AND m.role IN ('owner','admin','agent')
			 ORDER BY tm.last_assigned_at NULLS FIRST
			 LIMIT 1
			 FOR UPDATE OF tm SKIP LOCKED`,
			*in.TeamID, in.OrgID,
		).Scan(&picked); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrNoTeamMembers
			}
			return fmt.Errorf("pick assignee: %w", err)
		}

		if err := tx.QueryRow(ctx,
			`UPDATE tickets
			 SET assignee_id = $3,
			     team_id     = $4,
			     version     = version + 1,
			     updated_at  = now()
			 WHERE org_id = $1 AND number = $2 AND deleted_at IS NULL
			 RETURNING id, org_id, number, requester_id, assignee_id, team_id,
			           sla_policy_id, merged_into_id, subject, status, priority,
			           channel, first_response_due_at, resolution_due_at,
			           first_response_at, resolved_at, closed_at, version,
			           created_at, updated_at, deleted_at`,
			in.OrgID, in.Number, picked, *in.TeamID,
		).Scan(
			&out.ID, &out.OrgID, &out.Number, &out.RequesterID, &out.AssigneeID,
			&out.TeamID, &out.SLAPolicyID, &out.MergedIntoID, &out.Subject,
			&out.Status, &out.Priority, &out.Channel, &out.FirstResponseDueAt,
			&out.ResolutionDueAt, &out.FirstResponseAt, &out.ResolvedAt,
			&out.ClosedAt, &out.Version, &out.CreatedAt, &out.UpdatedAt, &out.DeletedAt,
		); err != nil {
			return fmt.Errorf("update ticket: %w", err)
		}

		if _, err := tx.Exec(ctx,
			`UPDATE team_members SET last_assigned_at = now()
			 WHERE team_id = $1 AND user_id = $2`,
			*in.TeamID, picked,
		); err != nil {
			return fmt.Errorf("update last_assigned_at: %w", err)
		}

		if err := Record(ctx, tx, in.OrgID, out.ID, in.Actor, "ticket.assigned", map[string]any{
			"old_assignee_id": oldAssignee,
			"new_assignee_id": picked,
			"old_team_id":     oldTeam,
			"new_team_id":     *in.TeamID,
			"auto":            true,
		}); err != nil {
			return err
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO ticket_watchers (ticket_id, user_id)
			 VALUES ($1, $2)
			 ON CONFLICT (ticket_id, user_id) DO NOTHING`,
			out.ID, picked,
		); err != nil {
			return fmt.Errorf("add watcher: %w", err)
		}

		if err := tx.QueryRow(ctx,
			`SELECT c.email, COALESCE(c.name, '') FROM customers c WHERE c.id = $1`,
			out.RequesterID,
		).Scan(&out.RequesterEmail, &out.RequesterName); err != nil {
			return fmt.Errorf("load requester: %w", err)
		}

		return nil
	})
	if err != nil {
		return Ticket{}, err
	}
	return out, nil
}

func (r *Repository) CreateTag(ctx context.Context, orgID uuid.UUID, name string) (Tag, error) {
	var t Tag
	err := r.pool.QueryRow(ctx,
		`INSERT INTO tags (org_id, name) VALUES ($1, $2)
		 RETURNING id, org_id, name, created_at`,
		orgID, name,
	).Scan(&t.ID, &t.OrgID, &t.Name, &t.CreatedAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return Tag{}, ErrTagNameTaken
		}
		return Tag{}, fmt.Errorf("create tag: %w", err)
	}
	return t, nil
}

func (r *Repository) ListTags(ctx context.Context, orgID uuid.UUID) ([]Tag, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT id, org_id, name, created_at
		 FROM tags WHERE org_id = $1 ORDER BY name`,
		orgID,
	)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	defer rows.Close()

	var out []Tag
	for rows.Next() {
		var t Tag
		if err := rows.Scan(&t.ID, &t.OrgID, &t.Name, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan tag: %w", err)
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("rows: %w", err)
	}
	return out, nil
}

// AttachTag adds a tag to a ticket by name. Idempotent: a second attach
// writes no event and returns nil.
func (r *Repository) AttachTag(ctx context.Context, orgID uuid.UUID, number int64, tagName string, actor Actor) error {
	return database.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		var ticketID uuid.UUID
		if err := tx.QueryRow(ctx,
			`SELECT id FROM tickets
			 WHERE org_id = $1 AND number = $2 AND deleted_at IS NULL`,
			orgID, number,
		).Scan(&ticketID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrTicketNotFound
			}
			return fmt.Errorf("find ticket: %w", err)
		}

		var tagID uuid.UUID
		if err := tx.QueryRow(ctx,
			`SELECT id FROM tags WHERE org_id = $1 AND name = $2`,
			orgID, tagName,
		).Scan(&tagID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrTagNotFound
			}
			return fmt.Errorf("find tag: %w", err)
		}

		tag, err := tx.Exec(ctx,
			`INSERT INTO ticket_tags (ticket_id, tag_id) VALUES ($1, $2)
			 ON CONFLICT (ticket_id, tag_id) DO NOTHING`,
			ticketID, tagID,
		)
		if err != nil {
			return fmt.Errorf("insert ticket_tag: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return nil
		}

		return Record(ctx, tx, orgID, ticketID, actor, "ticket.tagged", map[string]any{
			"tag": tagName,
		})
	})
}

// DetachTag removes a tag from a ticket by name. Idempotent: a second detach
// writes no event and returns nil.
func (r *Repository) DetachTag(ctx context.Context, orgID uuid.UUID, number int64, tagName string, actor Actor) error {
	return database.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		var ticketID uuid.UUID
		if err := tx.QueryRow(ctx,
			`SELECT id FROM tickets
			 WHERE org_id = $1 AND number = $2 AND deleted_at IS NULL`,
			orgID, number,
		).Scan(&ticketID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrTicketNotFound
			}
			return fmt.Errorf("find ticket: %w", err)
		}

		var tagID uuid.UUID
		if err := tx.QueryRow(ctx,
			`SELECT id FROM tags WHERE org_id = $1 AND name = $2`,
			orgID, tagName,
		).Scan(&tagID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrTagNotFound
			}
			return fmt.Errorf("find tag: %w", err)
		}

		tag, err := tx.Exec(ctx,
			`DELETE FROM ticket_tags WHERE ticket_id = $1 AND tag_id = $2`,
			ticketID, tagID,
		)
		if err != nil {
			return fmt.Errorf("delete ticket_tag: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return nil
		}

		return Record(ctx, tx, orgID, ticketID, actor, "ticket.untagged", map[string]any{
			"tag": tagName,
		})
	})
}

func (r *Repository) AddWatcher(ctx context.Context, orgID uuid.UUID, number int64, userID uuid.UUID) error {
	return database.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		var ticketID uuid.UUID
		if err := tx.QueryRow(ctx,
			`SELECT id FROM tickets
			 WHERE org_id = $1 AND number = $2 AND deleted_at IS NULL`,
			orgID, number,
		).Scan(&ticketID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrTicketNotFound
			}
			return fmt.Errorf("find ticket: %w", err)
		}

		var isMember bool
		if err := tx.QueryRow(ctx,
			`SELECT EXISTS (
			   SELECT 1 FROM memberships WHERE org_id = $1 AND user_id = $2
			 )`,
			orgID, userID,
		).Scan(&isMember); err != nil {
			return fmt.Errorf("check membership: %w", err)
		}
		if !isMember {
			return ErrMemberNotInOrg
		}

		if _, err := tx.Exec(ctx,
			`INSERT INTO ticket_watchers (ticket_id, user_id) VALUES ($1, $2)
			 ON CONFLICT (ticket_id, user_id) DO NOTHING`,
			ticketID, userID,
		); err != nil {
			return fmt.Errorf("insert watcher: %w", err)
		}
		return nil
	})
}

func (r *Repository) RemoveWatcher(ctx context.Context, orgID uuid.UUID, number int64, userID uuid.UUID) error {
	return database.WithTx(ctx, r.pool, func(tx pgx.Tx) error {
		var ticketID uuid.UUID
		if err := tx.QueryRow(ctx,
			`SELECT id FROM tickets
			 WHERE org_id = $1 AND number = $2 AND deleted_at IS NULL`,
			orgID, number,
		).Scan(&ticketID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ErrTicketNotFound
			}
			return fmt.Errorf("find ticket: %w", err)
		}

		_, err := tx.Exec(ctx,
			`DELETE FROM ticket_watchers WHERE ticket_id = $1 AND user_id = $2`,
			ticketID, userID,
		)
		if err != nil {
			return fmt.Errorf("delete watcher: %w", err)
		}
		return nil
	})
}
