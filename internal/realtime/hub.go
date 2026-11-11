package realtime

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Replayer loads missed ticket_events after a cursor. The repository implements
// it; tests can substitute an in-memory version.
type Replayer interface {
	Replay(ctx context.Context, orgID uuid.UUID, afterSeq int64, limit int) ([]Event, error)
}

// Sink is the transport side of one connection: an SSE writer in production, a
// recorder in tests.
type Sink interface {
	Event(ev Event) error
	Heartbeat() error
}

// HubOptions controls buffer sizes and heartbeat cadence. Zero values fall back
// to production defaults.
type HubOptions struct {
	Heartbeat   time.Duration
	BufferSize  int
	ReplayBatch int
}

// Hub owns the local connections, grouped by organisation. A connection gets a
// buffered channel and is written by its handler goroutine; broadcasting never
// blocks and a connection whose buffer is full is dropped so the client can
// reconnect and replay.
type Hub struct {
	backplane Backplane
	replayer  Replayer
	logger    *slog.Logger
	heartbeat time.Duration
	buffer    int
	batch     int

	ctx    context.Context
	cancel context.CancelFunc
	once   sync.Once
	wg     sync.WaitGroup

	mu   sync.Mutex
	orgs map[uuid.UUID]*orgState
}

type orgState struct {
	conns  map[*subscriber]struct{}
	cancel context.CancelFunc
}

type subscriber struct {
	send chan Event
	done chan struct{}
	once sync.Once
}

func (s *subscriber) close() {
	s.once.Do(func() { close(s.done) })
}

func NewHub(backplane Backplane, replayer Replayer, logger *slog.Logger, opts HubOptions) *Hub {
	if logger == nil {
		logger = slog.Default()
	}
	if opts.Heartbeat <= 0 {
		opts.Heartbeat = 20 * time.Second
	}
	if opts.BufferSize <= 0 {
		opts.BufferSize = 64
	}
	if opts.ReplayBatch <= 0 {
		opts.ReplayBatch = 200
	}

	ctx, cancel := context.WithCancel(context.Background())
	return &Hub{
		backplane: backplane,
		replayer:  replayer,
		logger:    logger,
		heartbeat: opts.Heartbeat,
		buffer:    opts.BufferSize,
		batch:     opts.ReplayBatch,
		ctx:       ctx,
		cancel:    cancel,
		orgs:      make(map[uuid.UUID]*orgState),
	}
}

// Close cancels every stream handler and waits for the per-org fan-out
// goroutines. The HTTP server calls it before Shutdown so long-lived handlers
// return instead of consuming the whole shutdown deadline.
func (h *Hub) Close() {
	h.once.Do(h.cancel)
	h.wg.Wait()
}

// Serve replays missed events, then writes live events until the client
// disconnects, the hub closes, or the connection is dropped as slow.
func (h *Hub) Serve(ctx context.Context, orgID uuid.UUID, afterSeq int64, sink Sink) error {
	if sink == nil {
		return errors.New("realtime: nil sink")
	}

	conn := &subscriber{
		send: make(chan Event, h.buffer),
		done: make(chan struct{}),
	}
	if err := h.register(orgID, conn); err != nil {
		return err
	}
	defer h.unregister(orgID, conn)

	lastSeq := afterSeq
	seenSize := h.buffer * 2
	if seenSize < 64 {
		seenSize = 64
	}
	seen := newSeqWindow(seenSize)
	if h.replayer != nil {
		for {
			events, err := h.replayer.Replay(ctx, orgID, lastSeq, h.batch)
			if err != nil {
				if ctx.Err() != nil || h.ctx.Err() != nil {
					return nil
				}
				// The live subscription is already active, so continuing is
				// better than terminating the stream. A reconnect can replay
				// anything this query could not read.
				h.logger.Warn("stream replay failed", "org_id", orgID, "error", err)
				break
			}
			for _, ev := range events {
				if ev.Seq <= lastSeq {
					continue
				}
				lastSeq = ev.Seq
				if !seen.add(ev.Seq) {
					continue
				}
				if err := sink.Event(ev); err != nil {
					return err
				}
			}
			if len(events) < h.batch {
				break
			}
		}
	}

	ticker := time.NewTicker(h.heartbeat)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-h.ctx.Done():
			return nil
		case <-conn.done:
			return nil
		case ev := <-conn.send:
			// Sequence numbers are assigned inside the writing transaction,
			// so commit (and therefore publish) order can differ from seq
			// order by a few positions. Skipping every seq <= lastSeq would
			// lose those events; skip only duplicates already sent on this
			// connection.
			if ev.Seq > 0 && !seen.add(ev.Seq) {
				continue
			}
			if err := sink.Event(ev); err != nil {
				return err
			}
		case <-ticker.C:
			if err := sink.Heartbeat(); err != nil {
				return err
			}
		}
	}
}

// seqWindow remembers a bounded set of recently sent sequence numbers. It is
// per connection and only used to suppress the overlap between replay and the
// live queue after a reconnect.
type seqWindow struct {
	size   int
	values []int64
	seen   map[int64]struct{}
	next   int
}

func newSeqWindow(size int) *seqWindow {
	if size < 8 {
		size = 8
	}
	// Allocation is deferred until the first event so an idle connection does
	// not pay for the window.
	return &seqWindow{size: size}
}

// add records seq and reports whether it was new.
func (w *seqWindow) add(seq int64) bool {
	if w.seen == nil {
		w.values = make([]int64, w.size)
		w.seen = make(map[int64]struct{}, w.size)
	}
	if _, ok := w.seen[seq]; ok {
		return false
	}
	if old := w.values[w.next]; old != 0 {
		delete(w.seen, old)
	}
	w.values[w.next] = seq
	w.next = (w.next + 1) % len(w.values)
	w.seen[seq] = struct{}{}
	return true
}

// Connections returns the number of local connections across all orgs.
func (h *Hub) Connections() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	total := 0
	for _, st := range h.orgs {
		total += len(st.conns)
	}
	return total
}

// Orgs returns the number of orgs with a live subscription.
func (h *Hub) Orgs() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.orgs)
}

func (h *Hub) register(orgID uuid.UUID, conn *subscriber) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	st := h.orgs[orgID]
	if st == nil {
		// Subscribe synchronously: the subscription must exist before the
		// replay query runs, otherwise an event committed in between would be
		// lost.
		sub, err := h.backplane.Subscribe(h.ctx, orgID)
		if err != nil {
			return err
		}
		orgCtx, cancel := context.WithCancel(h.ctx)
		st = &orgState{conns: make(map[*subscriber]struct{}), cancel: cancel}
		h.orgs[orgID] = st
		h.wg.Add(1)
		go h.fanout(orgCtx, orgID, st, sub)
	}

	st.conns[conn] = struct{}{}
	return nil
}

func (h *Hub) unregister(orgID uuid.UUID, conn *subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()

	st := h.orgs[orgID]
	if st == nil {
		return
	}
	if _, ok := st.conns[conn]; !ok {
		return
	}
	delete(st.conns, conn)
	if len(st.conns) == 0 {
		delete(h.orgs, orgID)
		st.cancel()
	}
}

func (h *Hub) fanout(ctx context.Context, orgID uuid.UUID, st *orgState, sub Subscription) {
	defer h.wg.Done()
	defer sub.Close()

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-sub.Events():
			if !ok {
				// Redis dropped the subscription. Remove it so the next
				// connection establishes a fresh one.
				h.removeOrg(orgID, st)
				return
			}
			h.broadcast(orgID, st, ev)
		}
	}
}

func (h *Hub) broadcast(orgID uuid.UUID, st *orgState, ev Event) {
	h.mu.Lock()
	defer h.mu.Unlock()

	for conn := range st.conns {
		select {
		case conn.send <- ev:
		default:
			// Never block the fan-out goroutine on a slow client. Closing the
			// connection makes its handler return; Last-Event-ID makes the
			// reconnect lossless.
			conn.close()
			delete(st.conns, conn)
		}
	}

	if len(st.conns) == 0 {
		if current, ok := h.orgs[orgID]; ok && current == st {
			delete(h.orgs, orgID)
		}
		st.cancel()
	}
}

func (h *Hub) removeOrg(orgID uuid.UUID, st *orgState) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if current, ok := h.orgs[orgID]; ok && current == st {
		delete(h.orgs, orgID)
	}
	st.cancel()
}
