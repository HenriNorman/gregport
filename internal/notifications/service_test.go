package notifications

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestComputeRecipients(t *testing.T) {
	assigneeID := uuid.New()
	watcherID := uuid.New()
	actorID := uuid.New()
	ticketID := uuid.New()

	tc := TicketContext{TicketID: ticketID, AssigneeID: &assigneeID}
	watchers := []uuid.UUID{watcherID}

	got := computeRecipients(tc, watchers, actorID)
	if len(got) != 2 {
		t.Fatalf("got %d recipients, want 2 (assignee, watcher): %v", len(got), got)
	}
	for _, id := range got {
		if id == actorID {
			t.Fatalf("actor %s should be excluded", actorID)
		}
	}

	// Actor is also the assignee: only the watcher remains.
	got = computeRecipients(tc, watchers, assigneeID)
	if len(got) != 1 || got[0] != watcherID {
		t.Fatalf("got %v, want [%s]", got, watcherID)
	}

	// No assignee or watchers: nobody to notify.
	got = computeRecipients(TicketContext{TicketID: ticketID}, nil, actorID)
	if len(got) != 0 {
		t.Fatalf("got %d recipients, want 0", len(got))
	}

	// Duplicate watcher/assignee collapses to one recipient.
	got = computeRecipients(tc, []uuid.UUID{assigneeID, watcherID, watcherID}, actorID)
	if len(got) != 2 {
		t.Fatalf("got %d recipients, want 2 after dedupe: %v", len(got), got)
	}
}

type fakeRepo struct {
	notifications map[uuid.UUID]map[uuid.UUID]Notification
	prefs         map[uuid.UUID][]Preference
	storedPrefs   []Preference

	listResult  []Notification
	listNext    string
	listErr     error
	lastListOrg uuid.UUID
	lastListUID uuid.UUID
	lastUnread  bool
	lastLimit   int
	lastCursor  string

	markReadErr  error
	markAllReadN int64
	lastMarkOrg  uuid.UUID
	lastMarkUID  uuid.UUID
	lastMarkID   uuid.UUID

	ticketCtx  TicketContext
	commentCtx *CommentContext
	watchers   []uuid.UUID
}

func (f *fakeRepo) LoadTicketContext(ctx context.Context, orgID, ticketID uuid.UUID) (TicketContext, error) {
	tc := f.ticketCtx
	if tc.OrgID == uuid.Nil {
		tc.OrgID = orgID
	}
	if tc.TicketID == uuid.Nil {
		tc.TicketID = ticketID
	}
	return tc, nil
}

func (f *fakeRepo) LoadComment(ctx context.Context, orgID, commentID uuid.UUID) (CommentContext, error) {
	if f.commentCtx != nil {
		return *f.commentCtx, nil
	}
	return CommentContext{ID: commentID}, nil
}

func (f *fakeRepo) ListWatchers(ctx context.Context, ticketID uuid.UUID) ([]uuid.UUID, error) {
	return f.watchers, nil
}

func (f *fakeRepo) InsertNotification(ctx context.Context, n Notification) (uuid.UUID, bool, error) {
	if f.notifications == nil {
		f.notifications = make(map[uuid.UUID]map[uuid.UUID]Notification)
	}
	if f.notifications[n.EventID] == nil {
		f.notifications[n.EventID] = make(map[uuid.UUID]Notification)
	}
	if _, exists := f.notifications[n.EventID][n.UserID]; exists {
		return n.ID, false, nil // dedupe: return existing
	}
	f.notifications[n.EventID][n.UserID] = n
	return n.ID, true, nil
}

func (f *fakeRepo) ListPreferences(ctx context.Context, userID uuid.UUID) ([]Preference, error) {
	return f.prefs[userID], nil
}

func (f *fakeRepo) ReplacePreferences(ctx context.Context, userID uuid.UUID, prefs []Preference) error {
	f.storedPrefs = prefs
	// Mirror the repository: only disabled rows are persisted.
	f.prefs = map[uuid.UUID][]Preference{}
	for _, p := range prefs {
		if !p.Enabled {
			f.prefs[userID] = append(f.prefs[userID], p)
		}
	}
	return nil
}

func (f *fakeRepo) MarkRead(ctx context.Context, orgID, userID, notificationID uuid.UUID) error {
	f.lastMarkOrg, f.lastMarkUID, f.lastMarkID = orgID, userID, notificationID
	return f.markReadErr
}

func (f *fakeRepo) MarkAllRead(ctx context.Context, orgID, userID uuid.UUID) (int64, error) {
	f.lastMarkOrg, f.lastMarkUID = orgID, userID
	return f.markAllReadN, nil
}

func (f *fakeRepo) ListNotifications(ctx context.Context, orgID, userID uuid.UUID, unreadOnly bool, limit int, cursor string) ([]Notification, string, error) {
	f.lastListOrg, f.lastListUID = orgID, userID
	f.lastUnread, f.lastLimit, f.lastCursor = unreadOnly, limit, cursor
	return f.listResult, f.listNext, f.listErr
}

func TestServiceFanOut(t *testing.T) {
	ctx := context.Background()
	orgID := uuid.New()
	eventID := uuid.New()
	assigneeID := uuid.New()
	watcherID := uuid.New()
	actorID := uuid.New()
	ticketID := uuid.New()
	commentID := uuid.New()

	tc := TicketContext{
		OrgID:      orgID,
		TicketID:   ticketID,
		Number:     1042,
		Subject:    "test ticket",
		AssigneeID: &assigneeID,
	}
	commentCtx := &CommentContext{
		ID:         commentID,
		TicketID:   ticketID,
		AuthorType: "customer",
		Body:       "hello",
		IsInternal: false,
	}

	repo := &fakeRepo{}
	svc := NewService(repo)

	n, err := svc.FanOut(ctx, orgID, eventID, EventNewComment, tc, commentCtx, []uuid.UUID{watcherID}, actorID)
	if err != nil {
		t.Fatalf("fanout: %v", err)
	}
	if n != 2 {
		t.Fatalf("inserted %d notifications, want 2 (assignee, watcher)", n)
	}

	// Replay: dedupe should insert zero.
	n, err = svc.FanOut(ctx, orgID, eventID, EventNewComment, tc, commentCtx, []uuid.UUID{watcherID}, actorID)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if n != 0 {
		t.Fatalf("replay inserted %d, want 0", n)
	}

	// The persisted notification carries the comment id and ticket payload.
	stored := repo.notifications[eventID][assigneeID]
	if stored.CommentID == nil || *stored.CommentID != commentID {
		t.Fatalf("comment id not persisted: %+v", stored)
	}
	if stored.TicketID == nil || *stored.TicketID != ticketID {
		t.Fatalf("ticket id not persisted: %+v", stored)
	}
	if len(stored.Payload) == 0 {
		t.Fatal("payload is empty")
	}
}

func TestServiceFanOutExcludesActor(t *testing.T) {
	ctx := context.Background()
	orgID := uuid.New()
	eventID := uuid.New()
	userID := uuid.New()
	ticketID := uuid.New()

	tc := TicketContext{OrgID: orgID, TicketID: ticketID, AssigneeID: &userID}

	repo := &fakeRepo{}
	svc := NewService(repo)

	// Actor is the only potential recipient - should be excluded.
	n, err := svc.FanOut(ctx, orgID, eventID, EventTicketAssigned, tc, nil, nil, userID)
	if err != nil {
		t.Fatalf("fanout: %v", err)
	}
	if n != 0 {
		t.Fatalf("inserted %d notifications, want 0 (actor excluded)", n)
	}
}

func TestServiceFanOutRespectsPreferences(t *testing.T) {
	ctx := context.Background()
	orgID := uuid.New()
	eventID := uuid.New()
	assigneeID := uuid.New()
	disabledUserID := uuid.New()
	ticketID := uuid.New()

	tc := TicketContext{OrgID: orgID, TicketID: ticketID, AssigneeID: &assigneeID}

	repo := &fakeRepo{
		prefs: map[uuid.UUID][]Preference{
			disabledUserID: {
				{UserID: disabledUserID, EventType: EventNewComment, Channel: ChannelInApp, Enabled: false},
			},
		},
	}
	svc := NewService(repo)

	n, err := svc.FanOut(ctx, orgID, eventID, EventNewComment, tc, nil, []uuid.UUID{disabledUserID}, uuid.New())
	if err != nil {
		t.Fatalf("fanout: %v", err)
	}
	if n != 1 {
		t.Fatalf("inserted %d notifications, want 1 (only assignee, disabled user skipped)", n)
	}
}

func TestServicePreferencesDefaultsToEnabled(t *testing.T) {
	svc := NewService(&fakeRepo{})
	prefs, err := svc.Preferences(context.Background(), uuid.New())
	if err != nil {
		t.Fatalf("preferences: %v", err)
	}
	want := len(EventTypes()) * len(Channels())
	if len(prefs) != want {
		t.Fatalf("got %d preferences, want %d", len(prefs), want)
	}
	for _, p := range prefs {
		if !p.Enabled {
			t.Fatalf("default preference disabled: %+v", p)
		}
	}
}

func TestServiceUpdatePreferences(t *testing.T) {
	ctx := context.Background()
	userID := uuid.New()
	repo := &fakeRepo{}
	svc := NewService(repo)

	prefs, err := svc.UpdatePreferences(ctx, userID, UpdatePreferencesRequest{
		Preferences: []PreferenceDTO{
			{EventType: EventNewComment, Channel: ChannelInApp, Enabled: false},
			{EventType: EventSLABreach, Channel: ChannelEmail, Enabled: true},
		},
	})
	if err != nil {
		t.Fatalf("update: %v", err)
	}

	// Only the disabled exception is persisted.
	if len(repo.storedPrefs) != 2 {
		t.Fatalf("stored %d preferences, want 2", len(repo.storedPrefs))
	}

	// The returned matrix keeps the full shape and reflects the override.
	seen := map[string]bool{}
	for _, p := range prefs {
		seen[p.EventType+"/"+p.Channel] = p.Enabled
	}
	if v, ok := seen[EventNewComment+"/"+ChannelInApp]; !ok || v {
		t.Fatalf("in_app new_comment should be disabled: %+v", seen)
	}
	if v, ok := seen[EventNewComment+"/"+ChannelEmail]; !ok || !v {
		t.Fatalf("email new_comment should stay enabled by default: %+v", seen)
	}
}

func TestServiceUpdatePreferencesValidation(t *testing.T) {
	svc := NewService(&fakeRepo{})
	cases := []UpdatePreferencesRequest{
		{Preferences: nil},
		{Preferences: []PreferenceDTO{{EventType: "bogus", Channel: ChannelInApp}}},
		{Preferences: []PreferenceDTO{{EventType: EventNewComment, Channel: "sms"}}},
		{Preferences: []PreferenceDTO{
			{EventType: EventNewComment, Channel: ChannelInApp},
			{EventType: EventNewComment, Channel: ChannelInApp},
		}},
	}
	for i, req := range cases {
		if _, err := svc.UpdatePreferences(context.Background(), uuid.New(), req); err == nil {
			t.Fatalf("case %d: expected validation error", i)
		}
	}
}

func TestServiceList(t *testing.T) {
	ctx := context.Background()
	orgID, userID := uuid.New(), uuid.New()
	ticketID := uuid.New()
	now := time.Now().UTC()

	repo := &fakeRepo{
		listResult: []Notification{{
			ID:        uuid.New(),
			OrgID:     orgID,
			UserID:    userID,
			Type:      EventNewComment,
			TicketID:  &ticketID,
			Payload:   []byte(`{"ticket_number":1042}`),
			CreatedAt: now,
		}},
		listNext: "next-cursor",
	}
	svc := NewService(repo)

	items, next, err := svc.List(ctx, orgID, userID, true, 10, "cursor-in")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 1 || next != "next-cursor" {
		t.Fatalf("items=%+v next=%q", items, next)
	}
	if repo.lastListOrg != orgID || repo.lastListUID != userID || !repo.lastUnread || repo.lastLimit != 10 || repo.lastCursor != "cursor-in" {
		t.Fatalf("repo args: org=%s user=%s unread=%v limit=%d cursor=%q",
			repo.lastListOrg, repo.lastListUID, repo.lastUnread, repo.lastLimit, repo.lastCursor)
	}
	if items[0].TicketID == nil || *items[0].TicketID != ticketID.String() {
		t.Fatalf("ticket id mapping: %+v", items[0])
	}
}

func TestServiceListPropagatesCursorError(t *testing.T) {
	repo := &fakeRepo{listErr: ErrInvalidCursor}
	svc := NewService(repo)
	_, _, err := svc.List(context.Background(), uuid.New(), uuid.New(), false, 25, "bad")
	if !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("got %v, want ErrInvalidCursor", err)
	}
}

func TestServiceMarkReadScopesToOrgAndUser(t *testing.T) {
	ctx := context.Background()
	orgID, userID, notifID := uuid.New(), uuid.New(), uuid.New()
	repo := &fakeRepo{}
	svc := NewService(repo)

	if err := svc.MarkRead(ctx, orgID, userID, notifID); err != nil {
		t.Fatalf("mark read: %v", err)
	}
	if repo.lastMarkOrg != orgID || repo.lastMarkUID != userID || repo.lastMarkID != notifID {
		t.Fatalf("mark args: %s %s %s", repo.lastMarkOrg, repo.lastMarkUID, repo.lastMarkID)
	}

	repo.markAllReadN = 3
	n, err := svc.MarkAllRead(ctx, orgID, userID)
	if err != nil || n != 3 {
		t.Fatalf("mark all: n=%d err=%v", n, err)
	}
}
