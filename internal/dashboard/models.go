package dashboard

// StatusCounts breaks tickets down by status.
type StatusCounts struct {
	New     int64 `json:"new"     example:"4"`
	Open    int64 `json:"open"    example:"7"`
	Pending int64 `json:"pending" example:"2"`
	Solved  int64 `json:"solved"  example:"31"`
	Closed  int64 `json:"closed"  example:"58"`
}

// PriorityCounts breaks tickets down by priority.
type PriorityCounts struct {
	Low    int64 `json:"low"    example:"12"`
	Normal int64 `json:"normal" example:"20"`
	High   int64 `json:"high"   example:"5"`
	Urgent int64 `json:"urgent" example:"1"`
}

// Counts is the dashboard summary for one organization.
type Counts struct {
	Total         int64          `json:"total"          example:"102"`
	Open          int64          `json:"open"           example:"13"`  // new + open + pending
	Unassigned    int64          `json:"unassigned"     example:"6"`   // open tickets with no assignee
	Overdue       int64          `json:"overdue"        example:"3"`   // unresolved tickets past resolution_due_at
	AverageRating float64        `json:"average_rating" example:"4.6"` // 0 when the org has no ratings
	RatingCount   int64          `json:"rating_count"   example:"18"`
	ByStatus      StatusCounts   `json:"by_status"`
	ByPriority    PriorityCounts `json:"by_priority"`
}
