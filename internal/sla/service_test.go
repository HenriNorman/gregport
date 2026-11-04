package sla

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fakeStore struct {
	policies  []Policy
	getErr    error
	listErr   error
	createErr error
	updateErr error
	deleteErr error

	creates []Policy
	updates []Policy
	deletes []uuid.UUID
}

func (f *fakeStore) List(_ context.Context, _ uuid.UUID) ([]Policy, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.policies, nil
}

func (f *fakeStore) Get(_ context.Context, _, policyID uuid.UUID) (Policy, error) {
	if f.getErr != nil {
		return Policy{}, f.getErr
	}
	for _, policy := range f.policies {
		if policy.ID == policyID {
			return policy, nil
		}
	}
	return Policy{}, ErrNotFound
}

func (f *fakeStore) Create(_ context.Context, orgID uuid.UUID, policy Policy) (Policy, error) {
	if f.createErr != nil {
		return Policy{}, f.createErr
	}
	policy.ID = uuid.New()
	policy.OrgID = orgID
	policy.CreatedAt = time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	f.creates = append(f.creates, policy)
	f.policies = append(f.policies, policy)
	return policy, nil
}

func (f *fakeStore) Update(_ context.Context, orgID uuid.UUID, policy Policy) (Policy, error) {
	if f.updateErr != nil {
		return Policy{}, f.updateErr
	}
	policy.OrgID = orgID
	f.updates = append(f.updates, policy)
	return policy, nil
}

func (f *fakeStore) Delete(_ context.Context, _, policyID uuid.UUID) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deletes = append(f.deletes, policyID)
	return nil
}

func intPtr(value int) *int { return &value }

func strPtr(value string) *string { return &value }

func TestCreatePolicyNormalizesAndPersists(t *testing.T) {
	store := &fakeStore{}
	svc := NewService(store)

	policy, err := svc.Create(context.Background(), uuid.New(), CreatePolicyRequest{
		Name:                 "  Urgent default  ",
		Priority:             "URGENT",
		FirstResponseMinutes: 15,
		ResolutionMinutes:    120,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if policy.Name != "Urgent default" {
		t.Errorf("name: %q", policy.Name)
	}
	if policy.Priority != "urgent" {
		t.Errorf("priority: %q", policy.Priority)
	}
	if len(store.creates) != 1 {
		t.Fatalf("persisted %d policies", len(store.creates))
	}
	if store.creates[0].Name != "Urgent default" || store.creates[0].Priority != "urgent" {
		t.Fatalf("persisted policy: %+v", store.creates[0])
	}
}

func TestCreatePolicyRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name string
		req  CreatePolicyRequest
	}{
		{"empty name", CreatePolicyRequest{Name: " ", Priority: "low", FirstResponseMinutes: 1, ResolutionMinutes: 1}},
		{"name too long", CreatePolicyRequest{Name: string(make([]byte, 101)), Priority: "low", FirstResponseMinutes: 1, ResolutionMinutes: 1}},
		{"unknown priority", CreatePolicyRequest{Name: "P", Priority: "critical", FirstResponseMinutes: 1, ResolutionMinutes: 1}},
		{"zero first response", CreatePolicyRequest{Name: "P", Priority: "low", FirstResponseMinutes: 0, ResolutionMinutes: 1}},
		{"negative first response", CreatePolicyRequest{Name: "P", Priority: "low", FirstResponseMinutes: -1, ResolutionMinutes: 1}},
		{"zero resolution", CreatePolicyRequest{Name: "P", Priority: "low", FirstResponseMinutes: 1, ResolutionMinutes: 0}},
		{"resolution before first response", CreatePolicyRequest{Name: "P", Priority: "low", FirstResponseMinutes: 30, ResolutionMinutes: 29}},
		{"first response over one year", CreatePolicyRequest{Name: "P", Priority: "low", FirstResponseMinutes: maxMinutes + 1, ResolutionMinutes: maxMinutes + 1}},
		{"resolution over one year", CreatePolicyRequest{Name: "P", Priority: "low", FirstResponseMinutes: 1, ResolutionMinutes: maxMinutes + 1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{}
			svc := NewService(store)

			if _, err := svc.Create(context.Background(), uuid.New(), tt.req); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
			if len(store.creates) != 0 {
				t.Fatalf("invalid policy was persisted")
			}
		})
	}
}

func TestCreatePolicyAllowsEqualAndBoundaryValues(t *testing.T) {
	svc := NewService(&fakeStore{})

	if _, err := svc.Create(context.Background(), uuid.New(), CreatePolicyRequest{
		Name:                 "Boundary",
		Priority:             "normal",
		FirstResponseMinutes: maxMinutes,
		ResolutionMinutes:    maxMinutes,
	}); err != nil {
		t.Fatalf("boundary values rejected: %v", err)
	}
}

func TestCreatePolicyPropagatesConflict(t *testing.T) {
	store := &fakeStore{createErr: ErrPolicyExists}
	svc := NewService(store)

	_, err := svc.Create(context.Background(), uuid.New(), CreatePolicyRequest{
		Name: "P", Priority: "low", FirstResponseMinutes: 1, ResolutionMinutes: 2,
	})
	if !errors.Is(err, ErrPolicyExists) {
		t.Fatalf("expected ErrPolicyExists, got %v", err)
	}
	if len(store.creates) != 0 {
		t.Fatal("conflicting policy was recorded as created")
	}
}

func TestUpdatePolicyMergesPartialFields(t *testing.T) {
	policyID := uuid.New()
	store := &fakeStore{policies: []Policy{{
		ID:                   policyID,
		Name:                 "High",
		Priority:             "high",
		FirstResponseMinutes: 30,
		ResolutionMinutes:    240,
	}}}
	svc := NewService(store)

	updated, err := svc.Update(context.Background(), uuid.New(), policyID, UpdatePolicyRequest{
		Name:                 strPtr("  High priority  "),
		FirstResponseMinutes: intPtr(15),
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Name != "High priority" {
		t.Errorf("name: %q", updated.Name)
	}
	if updated.Priority != "high" {
		t.Errorf("priority: %q", updated.Priority)
	}
	if updated.FirstResponseMinutes != 15 || updated.ResolutionMinutes != 240 {
		t.Errorf("minutes: %+v", updated)
	}
	if len(store.updates) != 1 {
		t.Fatalf("persisted %d updates", len(store.updates))
	}
}

func TestUpdatePolicyValidatesMergedResult(t *testing.T) {
	policyID := uuid.New()
	store := &fakeStore{policies: []Policy{{
		ID:                   policyID,
		Name:                 "High",
		Priority:             "high",
		FirstResponseMinutes: 30,
		ResolutionMinutes:    60,
	}}}
	svc := NewService(store)

	tests := []UpdatePolicyRequest{
		{ResolutionMinutes: intPtr(29)},            // below the existing first response
		{Priority: strPtr("critical")},             // invalid priority
		{Name: strPtr("")},                         // empty name
		{FirstResponseMinutes: intPtr(maxMinutes)}, // pushes first response above resolution
	}
	for i, req := range tests {
		if _, err := svc.Update(context.Background(), uuid.New(), policyID, req); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("case %d: expected ErrInvalidInput, got %v", i, err)
		}
	}
	if len(store.updates) != 0 {
		t.Fatalf("invalid updates were persisted: %+v", store.updates)
	}
}

func TestUpdatePolicyPropagatesNotFound(t *testing.T) {
	svc := NewService(&fakeStore{getErr: ErrNotFound})

	if _, err := svc.Update(context.Background(), uuid.New(), uuid.New(), UpdatePolicyRequest{}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestUpdatePolicyPropagatesConflict(t *testing.T) {
	policyID := uuid.New()
	store := &fakeStore{
		policies: []Policy{{
			ID: policyID, Name: "Low", Priority: "low",
			FirstResponseMinutes: 1, ResolutionMinutes: 2,
		}},
		updateErr: ErrPolicyExists,
	}
	svc := NewService(store)

	_, err := svc.Update(context.Background(), uuid.New(), policyID, UpdatePolicyRequest{
		Priority: strPtr("normal"),
	})
	if !errors.Is(err, ErrPolicyExists) {
		t.Fatalf("expected ErrPolicyExists, got %v", err)
	}
}

func TestDeletePolicyPropagatesInUse(t *testing.T) {
	store := &fakeStore{deleteErr: ErrPolicyInUse}
	svc := NewService(store)
	policyID := uuid.New()

	if err := svc.Delete(context.Background(), uuid.New(), policyID); !errors.Is(err, ErrPolicyInUse) {
		t.Fatalf("expected ErrPolicyInUse, got %v", err)
	}
	if len(store.deletes) != 0 {
		t.Fatal("in-use policy was recorded as deleted")
	}
}

func TestListAndGetDelegate(t *testing.T) {
	policyID := uuid.New()
	store := &fakeStore{policies: []Policy{{ID: policyID, Name: "Default"}}}
	svc := NewService(store)
	orgID := uuid.New()

	policies, err := svc.List(context.Background(), orgID)
	if err != nil || len(policies) != 1 {
		t.Fatalf("list: %+v err=%v", policies, err)
	}
	policy, err := svc.Get(context.Background(), orgID, policyID)
	if err != nil || policy.Name != "Default" {
		t.Fatalf("get: %+v err=%v", policy, err)
	}
}
