package commands

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"
)

// Stalwart (verified on 0.16.15) caches a successful HTTP Basic login in its
// "HTTP Authorization" cache, which has a size but no expiry. Until the entry
// is evicted or Stalwart restarts, a JMAP request (webmail included) with the
// same credentials is accepted without asking the SQL directory again. So
// after a password change the OLD password kept working over JMAP, and a
// disabled mailbox kept its JMAP access, while IMAP and SMTP refused both at
// once. The only action that clears it is InvalidateCaches, which drops every
// Stalwart cache.
//
// mailAuthCacheFlusher runs that action after a change that must cut a login
// off. Flushing every cache is not free, and a tenant can change passwords in
// a loop, so it runs at most once per mailAuthCacheMinGap: a request inside
// the gap schedules one trailing flush at the end of the gap, which still runs
// after the change that asked for it.
type mailAuthCacheFlusher struct {
	mu      sync.Mutex
	last    time.Time
	pending bool
	minGap  time.Duration
	now     func() time.Time
	after   func(time.Duration, func())
	flush   func(context.Context) error
}

const mailAuthCacheMinGap = 5 * time.Second

var mailAuthCache = &mailAuthCacheFlusher{
	minGap: mailAuthCacheMinGap,
	now:    time.Now,
	after:  func(d time.Duration, f func()) { time.AfterFunc(d, f) },
	flush: func(ctx context.Context) error {
		_, err := runStalwartCLI(ctx, "create", "Action/InvalidateCaches")
		return err
	},
}

// request flushes now, or schedules the trailing flush when one ran less than
// minGap ago. It reports the error of a flush it ran itself.
func (f *mailAuthCacheFlusher) request(ctx context.Context) error {
	f.mu.Lock()
	since := f.now().Sub(f.last)
	if f.last.IsZero() || since >= f.minGap {
		f.last = f.now()
		f.mu.Unlock()
		return f.flush(ctx)
	}
	if f.pending {
		f.mu.Unlock()
		return nil // the scheduled flush runs after this change too
	}
	f.pending = true
	f.mu.Unlock()
	f.after(f.minGap-since, func() {
		f.mu.Lock()
		f.pending = false
		f.last = f.now()
		f.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := f.flush(ctx); err != nil {
			slog.Warn("stalwart auth cache flush failed", "err", err)
		}
	})
	return nil
}

// flushMailAuthCache is the best-effort form the mailbox handlers use: the
// panel's database write already happened, so a failed flush is logged, not
// returned.
func flushMailAuthCache(ctx context.Context) {
	if err := mailAuthCache.request(ctx); err != nil {
		slog.Warn("stalwart auth cache flush failed", "err", err)
	}
}

// flushesMailAuthCache wraps the handler of a verb that ends or changes a
// mailbox login — a new password, a deleted mailbox, a purged or renamed
// domain — so the cache is flushed after every successful run. A wrapper
// rather than a call inside each handler, so an early "nothing to do" return
// cannot skip the flush.
func flushesMailAuthCache(h Handler) Handler {
	return func(ctx context.Context, params json.RawMessage) (any, error) {
		data, err := h(ctx, params)
		if err == nil {
			flushMailAuthCache(ctx)
		}
		return data, err
	}
}

// mail.auth_cache.flush — the panel calls it after a change that must end a
// mailbox's logins at once and that no other agent verb carries: a mailbox
// disabled, or (GH #1816) a domain sent back to pending. Takes no parameters.
func mailAuthCacheFlushHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	if err := mailAuthCache.request(ctx); err != nil {
		return nil, err
	}
	return okBody{Ok: true}, nil
}

func init() {
	Default.Register("mail.auth_cache.flush", mailAuthCacheFlushHandler)
}
