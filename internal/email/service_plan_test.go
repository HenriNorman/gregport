package email

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
)

func planFixture() (TicketContext, TaskPayload) {
	tc := TicketContext{
		OrgID:          uuid.New(),
		TicketID:       uuid.New(),
		Number:         1042,
		Subject:        "Cannot log in",
		RequesterEmail: "customer@acme.test",
		RequesterName:  "Ada Customer",
		AssigneeEmail:  "agent@acme.test",
		AssigneeName:   "Grace Agent",
		OrgName:        "Acme",
		SupportEmail:   "support@acme.test",
	}
	p := TaskPayload{
		EventID:   uuid.New(),
		OrgID:     tc.OrgID,
		TicketID:  tc.TicketID,
		EventType: EventTicketCreated,
		Data:      json.RawMessage(`{"number":1042}`),
	}
	return tc, p
}

func TestPlanDeliveriesRecipients(t *testing.T) {
	base, payload := planFixture()

	tests := []struct {
		name      string
		mutateTC  func(*TicketContext)
		eventType string
		comment   *CommentContext
		wantTo    string
		wantTmpl  string
	}{
		{
			name:      "ticket created goes to requester",
			eventType: EventTicketCreated,
			wantTo:    base.RequesterEmail,
			wantTmpl:  TemplateTicketCreated,
		},
		{
			name:      "ticket created with missing requester sends nothing",
			mutateTC:  func(tc *TicketContext) { tc.RequesterEmail = "  " },
			eventType: EventTicketCreated,
		},
		{
			name:      "agent comment goes to requester",
			eventType: EventCommentAdded,
			comment:   &CommentContext{AuthorType: "user", Body: "We are on it"},
			wantTo:    base.RequesterEmail,
			wantTmpl:  TemplateCommentAdded,
		},
		{
			name:      "system comment goes to requester",
			eventType: EventCommentAdded,
			comment:   &CommentContext{AuthorType: "system", Body: "Status changed"},
			wantTo:    base.RequesterEmail,
			wantTmpl:  TemplateCommentAdded,
		},
		{
			name:      "internal comment sends nothing",
			eventType: EventCommentAdded,
			comment:   &CommentContext{AuthorType: "user", Body: "secret", IsInternal: true},
		},
		{
			name:      "customer comment goes to assignee",
			eventType: EventCommentAdded,
			comment:   &CommentContext{AuthorType: "customer", Body: "Still broken"},
			wantTo:    base.AssigneeEmail,
			wantTmpl:  TemplateCommentAdded,
		},
		{
			name:      "customer comment without assignee falls back to support",
			mutateTC:  func(tc *TicketContext) { tc.AssigneeEmail = "" },
			eventType: EventCommentAdded,
			comment:   &CommentContext{AuthorType: "customer", Body: "Still broken"},
			wantTo:    base.SupportEmail,
			wantTmpl:  TemplateCommentAdded,
		},
		{
			name:      "customer comment without assignee or support sends nothing",
			mutateTC:  func(tc *TicketContext) { tc.AssigneeEmail = ""; tc.SupportEmail = "" },
			eventType: EventCommentAdded,
			comment:   &CommentContext{AuthorType: "customer", Body: "Still broken"},
		},
		{
			name:      "unknown comment author sends nothing",
			eventType: EventCommentAdded,
			comment:   &CommentContext{AuthorType: "robot", Body: "beep"},
		},
		{
			name:      "missing comment sends nothing",
			eventType: EventCommentAdded,
		},
		{
			name:      "assignment goes to new assignee",
			eventType: EventTicketAssigned,
			wantTo:    base.AssigneeEmail,
			wantTmpl:  TemplateTicketAssigned,
		},
		{
			name:      "unassignment sends nothing",
			mutateTC:  func(tc *TicketContext) { tc.AssigneeEmail = "" },
			eventType: EventTicketAssigned,
		},
		{
			name:      "sla breach goes to assignee",
			eventType: EventSLABreached,
			wantTo:    base.AssigneeEmail,
			wantTmpl:  TemplateSLABreached,
		},
		{
			name:      "sla breach falls back to support",
			mutateTC:  func(tc *TicketContext) { tc.AssigneeEmail = "" },
			eventType: EventSLABreached,
			wantTo:    base.SupportEmail,
			wantTmpl:  TemplateSLABreached,
		},
		{
			name:      "sla breach without any recipient sends nothing",
			mutateTC:  func(tc *TicketContext) { tc.AssigneeEmail = ""; tc.SupportEmail = "" },
			eventType: EventSLABreached,
		},
		{
			name:      "unknown event sends nothing",
			eventType: "ticket.updated",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tc := base
			p := payload
			p.EventType = tt.eventType
			if tt.mutateTC != nil {
				tt.mutateTC(&tc)
			}

			got := planDeliveries(tc, tt.comment, p)
			if tt.wantTo == "" {
				if len(got) != 0 {
					t.Fatalf("expected no deliveries, got %+v", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("deliveries: %+v", got)
			}
			if got[0].ToEmail != tt.wantTo || got[0].Template != tt.wantTmpl {
				t.Fatalf("delivery: %+v", got[0])
			}
			if got[0].Payload.TicketNumber != tc.Number ||
				got[0].Payload.Subject != tc.Subject ||
				got[0].Payload.OrgName != tc.OrgName ||
				got[0].Payload.EventType != tt.eventType {
				t.Fatalf("payload: %+v", got[0].Payload)
			}
			if tt.comment != nil && !tt.comment.IsInternal && got[0].Payload.CommentBody != tt.comment.Body {
				t.Fatalf("comment body: %q", got[0].Payload.CommentBody)
			}
			if string(got[0].Payload.EventData) != string(p.Data) {
				t.Fatalf("event data: %s", got[0].Payload.EventData)
			}
		})
	}
}

func TestTaskPayloadValidate(t *testing.T) {
	valid := TaskPayload{
		EventID: uuid.New(), OrgID: uuid.New(), TicketID: uuid.New(),
		EventType: EventTicketCreated,
	}

	tests := []struct {
		name    string
		mutate  func(*TaskPayload)
		wantErr bool
	}{
		{name: "valid"},
		{name: "missing event", mutate: func(p *TaskPayload) { p.EventID = uuid.Nil }, wantErr: true},
		{name: "missing org", mutate: func(p *TaskPayload) { p.OrgID = uuid.Nil }, wantErr: true},
		{name: "missing ticket", mutate: func(p *TaskPayload) { p.TicketID = uuid.Nil }, wantErr: true},
		{name: "missing type", mutate: func(p *TaskPayload) { p.EventType = "" }, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := valid
			if tt.mutate != nil {
				tt.mutate(&p)
			}
			err := p.Validate()
			if tt.wantErr && err == nil {
				t.Fatal("expected an error")
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}

	t.Run("nil data becomes empty object", func(t *testing.T) {
		p := valid
		p.Data = nil
		if err := p.Validate(); err != nil {
			t.Fatalf("validate: %v", err)
		}
		if string(p.Data) != `{}` {
			t.Fatalf("data: %s", p.Data)
		}
	})

	t.Run("json null becomes empty object", func(t *testing.T) {
		p := valid
		p.Data = json.RawMessage(`null`)
		if err := p.Validate(); err != nil {
			t.Fatalf("validate: %v", err)
		}
		if string(p.Data) != `{}` {
			t.Fatalf("data: %s", p.Data)
		}
	})
}

func TestCommentIDFromData(t *testing.T) {
	id := uuid.New()
	got, err := commentIDFromData(json.RawMessage(`{"comment_id":"` + id.String() + `"}`))
	if err != nil || got != id {
		t.Fatalf("comment id: %s, %v", got, err)
	}

	got, err = commentIDFromData(nil)
	if err != nil || got != uuid.Nil {
		t.Fatalf("empty data: %s, %v", got, err)
	}

	if _, err := commentIDFromData(json.RawMessage(`{"comment_id":123}`)); err == nil {
		t.Fatal("expected an error for a non-uuid comment id")
	}
}
