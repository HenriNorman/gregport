package tickets

import (
	"time"

	"github.com/google/uuid"
)

type Ticket struct {
	ID                 uuid.UUID
	OrgID              uuid.UUID
	Number             int64
	RequesterID        uuid.UUID
	AssigneeID         *uuid.UUID
	TeamID             *uuid.UUID
	SLAPolicyID        *uuid.UUID
	MergedIntoID       *uuid.UUID
	Subject            string
	Status             string
	Priority           string
	Channel            string
	FirstResponseDueAt *time.Time
	ResolutionDueAt    *time.Time
	FirstResponseAt    *time.Time
	ResolvedAt         *time.Time
	ClosedAt           *time.Time
	Version            int
	CreatedAt          time.Time
	UpdatedAt          time.Time
	DeletedAt          *time.Time
	RequesterEmail string
	RequesterName  string
}

type Comment struct {
	ID               uuid.UUID
	OrgID            uuid.UUID
	TicketID         uuid.UUID
	AuthorType       string
	AuthorUserID     *uuid.UUID
	AuthorCustomerID *uuid.UUID
	Body             string
	IsInternal       bool
	CreatedAt        time.Time
	DeletedAt        *time.Time
}

type Customer struct {
	ID         uuid.UUID
	OrgID      uuid.UUID
	Email      string
	Name       *string
	ExternalID *string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

type TicketEvent struct {
	ID        uuid.UUID
	OrgID     uuid.UUID
	TicketID  uuid.UUID
	ActorType string
	ActorID   *uuid.UUID
	EventType string
	Data      []byte   // jsonb
	CreatedAt time.Time
}

// Actor is who performed an action, for ticket_events.
type Actor struct {
	Type string     // "user", "customer", "api_key", "system"
	ID   *uuid.UUID
}

type Team struct{
	ID uuid.UUID
	OrgID uuid.UUID
	Name string
	CreatedAt time.Time
}

type TeamMember struct {
	TeamID 		uuid.UUID 
	UserID 		uuid.UUID
	LastAssignedAt 	*time.Time
}

type Tag struct{
	ID 		uuid.UUID
	OrgID 		uuid.UUID
	Name 		string 
	CreatedAt 	time.Time 
}

type Macro struct{
	ID 		uuid.UUID
	OrgID 		uuid.UUID
	Name 		string 
	Actions 	[]byte 
	CreatedBy 	uuid.UUID
	CreatedAT 	time.Time
	UpdatedAt 	time.Time
}

type MacroActions struct {
	SetStatus string   `json:"set_status,omitempty"`
	SetPriority string `json:"set_priority,omitempty"`
	AddTags   []string `json:"add_tags,omitempty"`
	Comment   string   `json:"comment,omitempty"`
}
