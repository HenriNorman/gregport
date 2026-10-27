package tickets

import "time"

// --- Create ---

type CreateTicketRequest struct {
	RequesterEmail string `json:"requester_email"`
	RequesterName  string `json:"requester_name"`
	Subject        string `json:"subject"`
	Body           string `json:"body"`
	Priority       string `json:"priority"`
}

// --- Update ---

type UpdateTicketRequest struct {
	Status   *string `json:"status,omitempty"`
	Priority *string `json:"priority,omitempty"`
	Subject  *string `json:"subject,omitempty"`
}

// --- Comments ---

type CreateCommentRequest struct {
	Body       string `json:"body"`
	IsInternal bool   `json:"is_internal"`
}

// --- Responses ---

type RequesterDTO struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

type TicketDTO struct {
	Number    int64        `json:"number"`
	ID        string       `json:"id"`
	Subject   string       `json:"subject"`
	Status    string       `json:"status"`
	Priority  string       `json:"priority"`
	Requester RequesterDTO `json:"requester"`
	Version   int          `json:"version"`
	CreatedAt time.Time    `json:"created_at"`
	UpdatedAt time.Time    `json:"updated_at"`
}

type CommentDTO struct {
	ID         string    `json:"id"`
	Body       string    `json:"body"`
	IsInternal bool      `json:"is_internal"`
	AuthorType string    `json:"author_type"`
	CreatedAt  time.Time `json:"created_at"`
}

type TicketListResponse struct {
	Tickets    []TicketDTO `json:"tickets"`
	NextCursor string      `json:"next_cursor,omitempty"`
}
