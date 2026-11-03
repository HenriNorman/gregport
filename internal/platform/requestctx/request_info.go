package requestctx

import (
	"context"
	"sync"
)

// RequestInfo is a mutable holder installed early in the middleware chain so
// that components running later (notably the access logger) can observe the
// principal once authentication has attached it to a derived context.
type RequestInfo struct {
	mu        sync.Mutex
	principal Principal
	ok        bool
}

type requestInfoKey struct{}

// WithRequestInfo installs a holder and returns it alongside the derived
// context.
func WithRequestInfo(ctx context.Context) (context.Context, *RequestInfo) {
	info := &RequestInfo{}
	return context.WithValue(ctx, requestInfoKey{}, info), info
}

// RequestInfoFromContext returns the holder installed by WithRequestInfo.
func RequestInfoFromContext(ctx context.Context) (*RequestInfo, bool) {
	info, ok := ctx.Value(requestInfoKey{}).(*RequestInfo)
	return info, ok
}

// Set records the authenticated principal.
func (i *RequestInfo) Set(p Principal) {
	if i == nil {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	i.principal = p
	i.ok = true
}

// Principal returns the last recorded principal.
func (i *RequestInfo) Principal() (Principal, bool) {
	if i == nil {
		return Principal{}, false
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.principal, i.ok
}
