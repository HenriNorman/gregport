package tickets

import (
	"context"
	"strings"
	"encoding/json"
	"fmt" 

	"github.com/google/uuid"
	"github.com/HenriNorman/gregport/internal/platform/requestctx"
)

type Service struct {
	repo *Repository
}

func NewService(repo *Repository) *Service {
	return &Service{repo: repo}
}

func (s *Service) CreateTicket(ctx context.Context, p requestctx.Principal, req CreateTicketRequest) (Ticket, error) {
	subject := strings.TrimSpace(req.Subject)
	if subject == "" || len(subject) > 500 {
		return Ticket{}, ErrInvalidInput
	}
	if len(req.Body) > 20_000 {
		return Ticket{}, ErrInvalidInput
	}
	if strings.TrimSpace(req.RequesterEmail) == "" {
		return Ticket{}, ErrInvalidInput
	}
	priority := req.Priority
	if priority == "" {
		priority = "normal"
	}
	if !validPriority(priority) {
		return Ticket{}, ErrInvalidInput
	}

	tk, _, err := s.repo.CreateTicket(ctx, CreateInput{
		OrgID:          p.OrgID,
		Actor:          Actor{Type: "user", ID: &p.UserID},
		RequesterEmail: strings.ToLower(strings.TrimSpace(req.RequesterEmail)),
		RequesterName:  strings.TrimSpace(req.RequesterName),
		Subject:        subject,
		Body:           req.Body,
		Priority:       priority,
	})
	return tk, err
}

func (s *Service) ListTickets(ctx context.Context, p requestctx.Principal, f ListFilter) (ListResult, error) {
	return s.repo.ListTickets(ctx, p.OrgID, f)
}

func (s *Service) GetTicket(ctx context.Context, p requestctx.Principal, number int64) (Ticket, error) {
	return s.repo.GetTicket(ctx, p.OrgID, number)
}

func (s *Service) UpdateTicket(ctx context.Context, p requestctx.Principal, number int64, version int, req UpdateTicketRequest) (Ticket, error) {
	if req.Status != nil && !validStatus(*req.Status) {
		return Ticket{}, ErrInvalidInput
	}
	if req.Priority != nil && !validPriority(*req.Priority) {
		return Ticket{}, ErrInvalidInput
	}
	if req.Subject != nil {
		v := strings.TrimSpace(*req.Subject)
		if v == "" || len(v) > 500 {
			return Ticket{}, ErrInvalidInput
		}
		req.Subject = &v
	}

	return s.repo.UpdateTicket(ctx, UpdateInput{
		OrgID:           p.OrgID,
		Number:          number,
		ExpectedVersion: version,
		Actor:           Actor{Type: "user", ID: &p.UserID},
		Status:          req.Status,
		Priority:        req.Priority,
		Subject:         req.Subject,
	})
}

func (s *Service) DeleteTicket(ctx context.Context, p requestctx.Principal, number int64) error {
	if !CanDelete(p) {
		return ErrForbidden
	}
	return s.repo.SoftDeleteTicket(ctx, p.OrgID, number, Actor{Type: "user", ID: &p.UserID})
}

func (s *Service) CreateComment(ctx context.Context, p requestctx.Principal, number int64, req CreateCommentRequest) (Comment, error) {
	body := strings.TrimSpace(req.Body)
	if body == "" || len(body) > 20_000 {
		return Comment{}, ErrInvalidInput
	}
	if req.IsInternal && p.Role != "owner" && p.Role != "admin" && p.Role != "agent" {
		return Comment{}, ErrForbidden
	}
	return s.repo.CreateComment(ctx, CreateCommentInput{
		OrgID:      p.OrgID,
		Number:     number,
		Actor:      Actor{Type: "user", ID: &p.UserID},
		Body:       body,
		IsInternal: req.IsInternal,
	})
}

func (s *Service) ListComments(ctx context.Context, p requestctx.Principal, number int64, includeInternal bool) ([]Comment, error) {
	if _, err := s.repo.GetTicket(ctx, p.OrgID, number); err != nil {
		return nil, err
	}
	return s.repo.ListComments(ctx, p.OrgID, number, includeInternal)
}

func validStatus(s string) bool {
	switch s {
	case "new", "open", "pending", "solved", "closed":
		return true
	}
	return false
}

func validPriority(s string) bool {
	switch s {
	case "low", "normal", "high", "urgent":
		return true
	}
	return false
}

func (s *Service) CreateTeam(ctx context.Context, p requestctx.Principal, name string) (Team, error) {
	if !CanManageTeams(p) {
		return Team{}, ErrForbidden
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return Team{}, ErrInvalidInput
	}
	return s.repo.CreateTeam(ctx, p.OrgID, name)
}

func (s *Service) ListTeams(ctx context.Context, p requestctx.Principal) ([]Team, error) {
	return s.repo.ListTeams(ctx, p.OrgID)
}

func (s *Service) UpdateTeam(ctx context.Context, p requestctx.Principal, teamID uuid.UUID, name string) (Team, error) {
	if !CanManageTeams(p) {
		return Team{}, ErrForbidden
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 100 {
		return Team{}, ErrInvalidInput
	}
	return s.repo.UpdateTeam(ctx, p.OrgID, teamID, name)
}

func (s *Service) DeleteTeam(ctx context.Context, p requestctx.Principal, teamID uuid.UUID) error {
	if !CanManageTeams(p) {
		return ErrForbidden
	}
	return s.repo.DeleteTeam(ctx, p.OrgID, teamID)
}

func (s *Service) AddTeamMember(ctx context.Context, p requestctx.Principal, teamID, userID uuid.UUID) error {
	if !CanManageTeams(p) {
		return ErrForbidden
	}
	return s.repo.AddTeamMember(ctx, p.OrgID, teamID, userID)
}

func (s *Service) RemoveTeamMember(ctx context.Context, p requestctx.Principal, teamID, userID uuid.UUID) error {
	if !CanManageTeams(p) {
		return ErrForbidden
	}
	return s.repo.RemoveTeamMember(ctx, p.OrgID, teamID, userID)
}

func (s *Service) AssignTicket(ctx context.Context, p requestctx.Principal, number int64, req AssignTicketRequest) (Ticket, error) {
	var assigneeID, teamID *uuid.UUID
	if req.AssigneeID != nil && *req.AssigneeID != "" {
		id, err := uuid.Parse(*req.AssigneeID)
		if err != nil {
			return Ticket{}, ErrInvalidInput
		}
		assigneeID = &id
	}
	if req.TeamID != nil && *req.TeamID != "" {
		id, err := uuid.Parse(*req.TeamID)
		if err != nil {
			return Ticket{}, ErrInvalidInput
		}
		teamID = &id
	}

	if req.Auto {
		if teamID == nil {
			return Ticket{}, ErrInvalidInput
		}
		return s.repo.AssignAuto(ctx, AssignInput{
			OrgID:  p.OrgID,
			Number: number,
			Actor:  Actor{Type: "user", ID: &p.UserID},
			TeamID: teamID,
		})
	}

	if req.Auto && assigneeID != nil {
		return Ticket{}, ErrInvalidInput
	}

	if assigneeID == nil && teamID == nil {
		return Ticket{}, ErrInvalidInput
	}

	return s.repo.AssignTicket(ctx, AssignInput{
		OrgID:      p.OrgID,
		Number:     number,
		Actor:      Actor{Type: "user", ID: &p.UserID},
		AssigneeID: assigneeID,
		TeamID:     teamID,
	})
}

func (s *Service) CreateTag(ctx context.Context, p requestctx.Principal, name string) (Tag, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if name == "" || len(name) > 50 {
		return Tag{}, ErrInvalidInput
	}
	return s.repo.CreateTag(ctx, p.OrgID, name)
}

func (s *Service) ListTags(ctx context.Context, p requestctx.Principal) ([]Tag, error) {
	return s.repo.ListTags(ctx, p.OrgID)
}

func (s *Service) AttachTag(ctx context.Context, p requestctx.Principal, number int64, tagName string) error {
	name := strings.ToLower(strings.TrimSpace(tagName))
	if name == "" {
		return ErrInvalidInput
	}
	return s.repo.AttachTag(ctx, p.OrgID, number, name, Actor{Type: "user", ID: &p.UserID})
}

func (s *Service) DetachTag(ctx context.Context, p requestctx.Principal, number int64, tagName string) error {
	name := strings.ToLower(strings.TrimSpace(tagName))
	if name == "" {
		return ErrInvalidInput
	}
	return s.repo.DetachTag(ctx, p.OrgID, number, name, Actor{Type: "user", ID: &p.UserID})
}

func (s *Service) AddWatcher(ctx context.Context, p requestctx.Principal, number int64, userID uuid.UUID) error {
	return s.repo.AddWatcher(ctx, p.OrgID, number, userID)
}

func (s *Service) RemoveWatcher(ctx context.Context, p requestctx.Principal, number int64, userID uuid.UUID) error {
	return s.repo.RemoveWatcher(ctx, p.OrgID, number, userID)
}

func (s *Service) ListEvents(ctx context.Context, p requestctx.Principal, number int64, cursor string, limit int) ([]TicketEvent, string, error) {
	if _, err := s.repo.GetTicket(ctx, p.OrgID, number); err != nil {
		return nil, "", err
	}
	return s.repo.ListTicketEvents(ctx, p.OrgID, number, cursor, limit)
}

func (s *Service) MergeTickets(ctx context.Context, p requestctx.Principal, sourceNumber, targetNumber int64) (Ticket, Ticket, error) {
	if sourceNumber == targetNumber {
		return Ticket{}, Ticket{}, ErrMergeSelf
	}
	return s.repo.MergeTickets(ctx, p.OrgID, sourceNumber, targetNumber,
		Actor{Type: "user", ID: &p.UserID})
}

func (s *Service) CreateMacro(ctx context.Context, p requestctx.Principal, req CreateMacroRequest) (Macro, error) {
	if !CanManageTeams(p) {
		return Macro{}, ErrForbidden
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 100 {
		return Macro{}, ErrInvalidInput
	}
	actions, err := validateActions(req.Actions)
	if err != nil {
		return Macro{}, err
	}
	return s.repo.CreateMacro(ctx, p.OrgID, p.UserID, name, actions)
}

func (s *Service) ListMacros(ctx context.Context, p requestctx.Principal) ([]Macro, error) {
	return s.repo.ListMacros(ctx, p.OrgID)
}

func (s *Service) UpdateMacro(ctx context.Context, p requestctx.Principal, macroID uuid.UUID, req UpdateMacroRequest) (Macro, error) {
	if !CanManageTeams(p) {
		return Macro{}, ErrForbidden
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || len(name) > 100 {
		return Macro{}, ErrInvalidInput
	}
	actions, err := validateActions(req.Actions)
	if err != nil {
		return Macro{}, err
	}
	return s.repo.UpdateMacro(ctx, p.OrgID, macroID, name, actions)
}

func (s *Service) DeleteMacro(ctx context.Context, p requestctx.Principal, macroID uuid.UUID) error {
	if !CanManageTeams(p) {
		return ErrForbidden
	}
	return s.repo.DeleteMacro(ctx, p.OrgID, macroID)
}

// ApplyMacro applies a macro's actions in order: status, priority, tags,
// comment. Each action calls the same service method as the equivalent
// endpoint, so the state machine, events, and authorization apply. The
// application is not atomic: if a later action fails, earlier actions stay
// applied. Ordering status first means an illegal transition fails before
// anything is written.
func (s *Service) ApplyMacro(ctx context.Context, p requestctx.Principal, number int64, macroID uuid.UUID) error {
	m, err := s.repo.GetMacro(ctx, p.OrgID, macroID)
	if err != nil {
		return err
	}

	var actions MacroActions
	if err := json.Unmarshal(m.Actions, &actions); err != nil {
		return fmt.Errorf("decode macro actions: %w", err)
	}

	if actions.SetStatus != "" {
		tk, err := s.repo.GetTicket(ctx, p.OrgID, number)
		if err != nil {
			return err
		}
		if _, err := s.UpdateTicket(ctx, p, number, tk.Version, UpdateTicketRequest{
			Status: &actions.SetStatus,
		}); err != nil {
			return err
		}
	}

	if actions.SetPriority != "" {
		tk, err := s.repo.GetTicket(ctx, p.OrgID, number)
		if err != nil {
			return err
		}
		if _, err := s.UpdateTicket(ctx, p, number, tk.Version, UpdateTicketRequest{
			Priority: &actions.SetPriority,
		}); err != nil {
			return err
		}
	}

	for _, tag := range actions.AddTags {
		if err := s.AttachTag(ctx, p, number, tag); err != nil {
			return err
		}
	}

	if actions.Comment != "" {
		if _, err := s.CreateComment(ctx, p, number, CreateCommentRequest{
			Body: actions.Comment,
		}); err != nil {
			return err
		}
	}

	return nil
}

func validateActions(a MacroActionsDTO) ([]byte, error) {
	if a.SetStatus != "" && !validStatus(a.SetStatus) {
		return nil, ErrInvalidInput
	}
	if a.SetPriority != "" && !validPriority(a.SetPriority) {
		return nil, ErrInvalidInput
	}
	for _, t := range a.AddTags {
		if strings.TrimSpace(t) == "" {
			return nil, ErrInvalidInput
		}
	}
	if len(a.Comment) > 20_000 {
		return nil, ErrInvalidInput
	}
	return json.Marshal(MacroActions{
		SetStatus:   a.SetStatus,
		SetPriority: a.SetPriority,
		AddTags:     a.AddTags,
		Comment:     a.Comment,
	})
}
