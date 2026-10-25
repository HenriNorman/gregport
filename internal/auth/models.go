package auth

import (
	"time"

	"github.com/google/uuid"
)

type Organization struct {
	ID        uuid.UUID
	Name      string
	Slug      string
	Plan      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type User struct {
	ID           uuid.UUID
	Email        string
	PasswordHash string
	Name         string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type Membership struct {
	ID        uuid.UUID
	OrgID     uuid.UUID
	UserID    uuid.UUID
	Role      string
	CreatedAt time.Time
}

type RefreshToken struct {
	ID        uuid.UUID
	UserID    uuid.UUID
	OrgID     uuid.UUID
	FamilyID  uuid.UUID
	TokenHash string
	UserAgent string
	IP        string
	ExpiresAt time.Time
	RevokedAt *time.Time
	CreatedAt time.Time
}

type MembershipInfo struct {
	OrgID   uuid.UUID
	OrgSlug string
	OrgName string
	Role    string
}

type LoginCandidate struct {
	UserID       uuid.UUID
	Email        string
	PasswordHash string
	Name         string
	Memberships  []MembershipInfo
}
