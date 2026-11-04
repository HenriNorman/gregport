package organizations

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

type fakeStore struct {
	settings  Settings
	updatedAt time.Time
	getErr    error
	updateErr error

	getCalls int
	updates  []Settings
}

func (f *fakeStore) GetSettings(_ context.Context, _ uuid.UUID) (Settings, time.Time, error) {
	f.getCalls++
	if f.getErr != nil {
		return Settings{}, time.Time{}, f.getErr
	}
	return f.settings, f.updatedAt, nil
}

func (f *fakeStore) UpdateSettings(_ context.Context, _ uuid.UUID, settings Settings) (Settings, time.Time, error) {
	if f.updateErr != nil {
		return Settings{}, time.Time{}, f.updateErr
	}
	f.settings = settings
	f.updatedAt = f.updatedAt.Add(time.Minute)
	f.updates = append(f.updates, settings)
	return settings, f.updatedAt, nil
}

func strPtr(value string) *string { return &value }

func TestGetNormalizesDefaultSettings(t *testing.T) {
	updatedAt := time.Date(2026, 11, 4, 9, 30, 0, 0, time.UTC)
	store := &fakeStore{updatedAt: updatedAt}
	svc := NewService(store)

	resp, err := svc.Get(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if resp.Settings.Timezone != "UTC" {
		t.Errorf("timezone: %q", resp.Settings.Timezone)
	}
	if resp.Settings.DefaultPriority != "normal" {
		t.Errorf("priority: %q", resp.Settings.DefaultPriority)
	}
	if resp.Settings.SupportEmail != "" {
		t.Errorf("support email: %q", resp.Settings.SupportEmail)
	}
	if !resp.UpdatedAt.Equal(updatedAt) {
		t.Errorf("updated at: %s", resp.UpdatedAt)
	}
	if store.getCalls != 1 {
		t.Errorf("get calls: %d", store.getCalls)
	}
}

func TestGetPropagatesErrors(t *testing.T) {
	store := &fakeStore{getErr: ErrOrgNotFound}
	svc := NewService(store)

	if _, err := svc.Get(context.Background(), uuid.New()); !errors.Is(err, ErrOrgNotFound) {
		t.Fatalf("expected ErrOrgNotFound, got %v", err)
	}
}

func TestUpdateMergesPartialIntoExistingSettings(t *testing.T) {
	store := &fakeStore{settings: Settings{
		Timezone:        "UTC",
		DefaultPriority: "normal",
		SupportEmail:    "support@acme.com",
	}}
	svc := NewService(store)

	resp, err := svc.Update(context.Background(), uuid.New(), UpdateSettingsRequest{
		Timezone: strPtr("Africa/Nairobi"),
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if resp.Settings.Timezone != "Africa/Nairobi" {
		t.Errorf("timezone: %q", resp.Settings.Timezone)
	}
	if resp.Settings.DefaultPriority != "normal" {
		t.Errorf("priority was not preserved: %q", resp.Settings.DefaultPriority)
	}
	if resp.Settings.SupportEmail != "support@acme.com" {
		t.Errorf("email was not preserved: %q", resp.Settings.SupportEmail)
	}
	if len(store.updates) != 1 {
		t.Fatalf("persisted %d times", len(store.updates))
	}
}

func TestUpdateNormalizesInput(t *testing.T) {
	store := &fakeStore{settings: Settings{Timezone: "UTC", DefaultPriority: "normal"}}
	svc := NewService(store)

	resp, err := svc.Update(context.Background(), uuid.New(), UpdateSettingsRequest{
		Timezone:        strPtr("  Africa/Nairobi  "),
		DefaultPriority: strPtr("URGENT"),
		SupportEmail:    strPtr("  Support@Acme.COM  "),
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if resp.Settings.Timezone != "Africa/Nairobi" {
		t.Errorf("timezone: %q", resp.Settings.Timezone)
	}
	if resp.Settings.DefaultPriority != "urgent" {
		t.Errorf("priority: %q", resp.Settings.DefaultPriority)
	}
	if resp.Settings.SupportEmail != "support@acme.com" {
		t.Errorf("email: %q", resp.Settings.SupportEmail)
	}
	if store.updates[0] != resp.Settings {
		t.Errorf("persisted %+v, returned %+v", store.updates[0], resp.Settings)
	}
}

func TestUpdateCanClearSupportEmail(t *testing.T) {
	store := &fakeStore{settings: Settings{
		Timezone:        "UTC",
		DefaultPriority: "normal",
		SupportEmail:    "support@acme.com",
	}}
	svc := NewService(store)

	resp, err := svc.Update(context.Background(), uuid.New(), UpdateSettingsRequest{
		SupportEmail: strPtr(""),
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if resp.Settings.SupportEmail != "" {
		t.Fatalf("support email was not cleared: %q", resp.Settings.SupportEmail)
	}
}

func TestUpdateRejectsInvalidSettings(t *testing.T) {
	tests := []struct {
		name string
		req  UpdateSettingsRequest
	}{
		{"unknown timezone", UpdateSettingsRequest{Timezone: strPtr("Mars/Olympus")}},
		{"unknown priority", UpdateSettingsRequest{DefaultPriority: strPtr("critical")}},
		{"malformed email", UpdateSettingsRequest{SupportEmail: strPtr("not-an-email")}},
		{"email with display name", UpdateSettingsRequest{SupportEmail: strPtr("Support <support@acme.com>")}},
		{"timezone too long", UpdateSettingsRequest{Timezone: strPtr("abcdefghijklmnopqrstuvwxyz/abcdefghijklmnopqrstuvwxyz/abcdefghijklmn")}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &fakeStore{settings: Settings{Timezone: "UTC", DefaultPriority: "normal"}}
			svc := NewService(store)

			if _, err := svc.Update(context.Background(), uuid.New(), tt.req); !errors.Is(err, ErrInvalidInput) {
				t.Fatalf("expected ErrInvalidInput, got %v", err)
			}
			if len(store.updates) != 0 {
				t.Fatalf("invalid settings were persisted: %+v", store.updates)
			}
		})
	}
}

func TestUpdatePropagatesStoreErrors(t *testing.T) {
	want := errors.New("database down")
	store := &fakeStore{
		settings:  Settings{Timezone: "UTC", DefaultPriority: "normal"},
		updateErr: want,
	}
	svc := NewService(store)

	if _, err := svc.Update(context.Background(), uuid.New(), UpdateSettingsRequest{
		Timezone: strPtr("Europe/London"),
	}); !errors.Is(err, want) {
		t.Fatalf("expected store error, got %v", err)
	}
}
