package inboundemail

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type fakeRepo struct {
	mu            sync.Mutex
	orgs          map[uuid.UUID]bool
	emails        map[string]InboundEmailRow // keyed by message_id
	ticketByKey   map[string]uuid.UUID       // key orgID:number
	ticketsByID   map[uuid.UUID]int64        // ticket id -> number
	customers     map[string]uuid.UUID       // key orgID:email
	nextTicket    int
	comments      []commentRecord
	insertCalls   []InsertInboundCall
	resolveOrgErr error
}

type commentRecord struct {
	OrgID      uuid.UUID
	TicketID   uuid.UUID
	CustomerID uuid.UUID
	Body       string
}

type InsertInboundCall struct {
	OrgID     uuid.UUID
	MessageID string
	Status    string
	TicketID  *uuid.UUID
	CommentID *uuid.UUID
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		orgs:        make(map[uuid.UUID]bool),
		emails:      make(map[string]InboundEmailRow),
		ticketByKey: make(map[string]uuid.UUID),
		customers:   make(map[string]uuid.UUID),
		ticketsByID: make(map[uuid.UUID]int64),
	}
}

func (f *fakeRepo) withOrg(orgID uuid.UUID) {
	f.mu.Lock()
	f.orgs[orgID] = true
	f.mu.Unlock()
}

func (f *fakeRepo) InsertInbound(_ context.Context, orgID uuid.UUID, e InboundEmail, status string, ticketID, commentID *uuid.UUID) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, exists := f.emails[e.MessageID]; exists {
		return ErrDuplicateMessageID
	}
	row := InboundEmailRow{
		ID:         uuid.New(),
		OrgID:      orgID,
		MessageID:  e.MessageID,
		InReplyTo:  e.InReplyTo,
		FromEmail:  e.FromEmail,
		FromName:   e.FromName,
		Recipient:  e.Recipient,
		Subject:    e.Subject,
		Body:       e.Body,
		Status:     status,
		TicketID:   ticketID,
		CommentID:  commentID,
		ReceivedAt: time.Now().UTC(),
	}
	f.emails[e.MessageID] = row
	if ticketID != nil {
		f.ticketsByID[*ticketID] = 0
	}
	f.insertCalls = append(f.insertCalls, InsertInboundCall{OrgID: orgID, MessageID: e.MessageID, Status: status, TicketID: ticketID, CommentID: commentID})
	return nil
}

func (f *fakeRepo) FindByMessageID(_ context.Context, messageID string) (InboundEmailRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.emails[messageID]
	if !ok {
		return InboundEmailRow{}, pgx.ErrNoRows
	}
	return row, nil
}

func (f *fakeRepo) ResolveOrg(_ context.Context, recipient string) (uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.resolveOrgErr != nil {
		return uuid.Nil, f.resolveOrgErr
	}
	local := recipient
	if at := strings.LastIndexByte(local, '@'); at >= 0 {
		local = local[:at]
	}
	if plus := strings.LastIndexByte(local, '+'); plus >= 0 {
		local = local[plus+1:]
	}
	local = strings.TrimSpace(local)
	orgID, err := uuid.Parse(local)
	if err != nil {
		return uuid.Nil, ErrOrgNotFound
	}
	if !f.orgs[orgID] {
		return uuid.Nil, ErrOrgNotFound
	}
	return orgID, nil
}

func (f *fakeRepo) FindOrCreateCustomer(_ context.Context, orgID uuid.UUID, email, _ string) (uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := orgID.String() + ":" + email
	if id, ok := f.customers[key]; ok {
		return id, nil
	}
	id := uuid.New()
	f.customers[key] = id
	return id, nil
}

func (f *fakeRepo) FindTicketByNumber(_ context.Context, orgID uuid.UUID, number int64) (uuid.UUID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := orgID.String() + ":" + strconv.FormatInt(number, 10)
	id, ok := f.ticketByKey[key]
	if !ok {
		return uuid.Nil, ErrTicketNotFound
	}
	return id, nil
}

func (f *fakeRepo) CreateTicket(_ context.Context, orgID, customerID uuid.UUID, _ string, _ string, subject, _ string) (Ticket, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextTicket++
	id := uuid.New()
	number := int64(1000 + f.nextTicket)
	f.ticketByKey[orgID.String()+":"+strconv.FormatInt(number, 10)] = id
	f.ticketsByID[id] = number
	f.comments = append(f.comments, commentRecord{OrgID: orgID, TicketID: id, CustomerID: customerID, Body: subject})
	_ = subject
	return Ticket{ID: id, Number: number}, nil
}

func (f *fakeRepo) AddComment(_ context.Context, _ uuid.UUID, ticketID, _ uuid.UUID, body string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.ticketsByID[ticketID]; !ok {
		return ErrTicketNotFound
	}
	f.comments = append(f.comments, commentRecord{TicketID: ticketID, Body: body})
	return nil
}

func (f *fakeRepo) seedOrg(orgID uuid.UUID) { f.withOrg(orgID) }

func (f *fakeRepo) seedTicket(orgID uuid.UUID, number int64) uuid.UUID {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := uuid.New()
	f.ticketByKey[orgID.String()+":"+strconv.FormatInt(number, 10)] = id
	f.ticketsByID[id] = number
	return id
}

func (f *fakeRepo) seedEmail(orgID uuid.UUID, e InboundEmailRow) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.emails[e.MessageID] = e
}

func (f *fakeRepo) lastInsert() InsertInboundCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	calls := f.insertCalls
	return calls[len(calls)-1]
}

var _ = context.Background
var _ = time.Now
