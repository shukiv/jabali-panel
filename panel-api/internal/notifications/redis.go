package notifications

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// Stream + consumer-group names. Public so callers (tests, the producer
// path in the API layer) can target the same keys without mis-typing.
const (
	StreamQueue = "jabali:notifications:queue"
	StreamDLQ   = "jabali:notifications:dlq"
	// Stream length caps (approximate, ~ trimming): a stuck channel or a
	// producer flood can otherwise grow these unbounded (a real install hit
	// 26549 DLQ entries). Approximate trimming is O(1)-ish on XADD.
	maxQueueLen    int64 = 20000
	maxDLQLen      int64 = 5000
	ConsumerGroup        = "dispatcher"
	defaultRetries       = 5
)

// Queue wraps the Redis client with typed operations the dispatcher
// needs. Keeps the dispatcher code free of Redis field-map plumbing.
type Queue struct {
	rdb *redis.Client
}

// NewQueue returns a Queue backed by the given client. The client is
// expected to be pre-pinged — redis.go in serve.go fails the boot if
// Redis is unreachable, so by the time NewQueue is called the client
// is known good.
func NewQueue(rdb *redis.Client) *Queue { return &Queue{rdb: rdb} }

// EnsureGroup creates the consumer group at the stream's tail,
// creating the stream if missing (MKSTREAM). BUSYGROUP is swallowed —
// it means the group already exists, which is the idempotent success
// case for a process that's restarting against state from a previous
// run.
func (q *Queue) EnsureGroup(ctx context.Context) error {
	err := q.rdb.XGroupCreateMkStream(ctx, StreamQueue, ConsumerGroup, "$").Err()
	if err == nil {
		return nil
	}
	// go-redis returns an untyped error with the literal "BUSYGROUP"
	// token inside; match on substring rather than by Is() since
	// there's no sentinel for it.
	if strings.Contains(err.Error(), "BUSYGROUP") {
		return nil
	}
	return err
}

// Publish enqueues one envelope. Returns the Redis-assigned ID so
// producers can log + correlate with history rows.
func (q *Queue) Publish(ctx context.Context, env Envelope) (string, error) {
	// Self-heal the consumer group before adding. If Redis lost its data
	// (restart without persistence, flush, or maxmemory eviction), the group
	// vanishes; a bare XAdd recreates only the stream key, leaving the
	// dispatcher's XReadGroup to NOGROUP indefinitely and the server-status
	// probe to show a scary banner. EnsureGroup is idempotent (BUSYGROUP
	// swallowed) and creates the group at the tail BEFORE this message is
	// appended, so the message stays consumable. Best-effort: a genuine Redis
	// outage surfaces on the XAdd below.
	_ = q.EnsureGroup(ctx)
	id, err := q.rdb.XAdd(ctx, &redis.XAddArgs{
		Stream: StreamQueue,
		MaxLen: maxQueueLen,
		Approx: true,
		Values: env.StreamMap(),
	}).Result()
	if err != nil {
		return "", err
	}
	return id, nil
}

// Read blocks up to block for next batch from the consumer's
// pending-entry-list + stream tail. Returns nil slice on BLOCK timeout
// (redis.Nil); treat as a normal "no work yet" loop iteration.
func (q *Queue) Read(ctx context.Context, consumer string, batch int, block time.Duration) ([]redis.XStream, error) {
	res, err := q.rdb.XReadGroup(ctx, &redis.XReadGroupArgs{
		Group:    ConsumerGroup,
		Consumer: consumer,
		Streams:  []string{StreamQueue, ">"},
		Count:    int64(batch),
		Block:    block,
	}).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	// The group/stream was lost out from under us (Redis wipe). Recreate it and
	// treat this tick as "no work"; the next Read consumes normally. Without
	// this the consumer loop spin-errors on NOGROUP until a panel restart.
	if err != nil && strings.Contains(err.Error(), "NOGROUP") {
		_ = q.EnsureGroup(ctx)
		return nil, nil
	}
	return res, err
}

// Ack + Del finalise a successfully-processed entry. Both happen in a
// single pipeline to avoid a torn state where ACK succeeds but DEL
// doesn't (the entry would reappear in XPENDING listings as "acked
// but still present", confusing the reclaim loop).
func (q *Queue) AckAndDelete(ctx context.Context, ids ...string) error {
	if len(ids) == 0 {
		return nil
	}
	ackIds := make([]any, len(ids))
	for i, id := range ids {
		ackIds[i] = id
	}
	_ = ackIds // retained for clarity; the XAck call takes variadic strings directly.
	pipe := q.rdb.TxPipeline()
	pipe.XAck(ctx, StreamQueue, ConsumerGroup, ids...)
	pipe.XDel(ctx, StreamQueue, ids...)
	_, err := pipe.Exec(ctx)
	return err
}

// Pending returns entries in the consumer group's PEL idle for at
// least minIdle. The dispatcher's reclaim loop uses this to surface
// entries stuck on a dead consumer (panel-api crashed mid-delivery).
func (q *Queue) Pending(ctx context.Context, minIdle time.Duration, count int64) ([]redis.XPendingExt, error) {
	return q.rdb.XPendingExt(ctx, &redis.XPendingExtArgs{
		Stream: StreamQueue,
		Group:  ConsumerGroup,
		Idle:   minIdle,
		Start:  "-",
		End:    "+",
		Count:  count,
	}).Result()
}

// Claim transfers pending entries to this consumer. Called by the
// reclaim loop for any PEL entry whose owner has gone away.
func (q *Queue) Claim(ctx context.Context, consumer string, minIdle time.Duration, ids ...string) ([]redis.XMessage, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	return q.rdb.XClaim(ctx, &redis.XClaimArgs{
		Stream:   StreamQueue,
		Group:    ConsumerGroup,
		Consumer: consumer,
		MinIdle:  minIdle,
		Messages: ids,
	}).Result()
}

// RangeOne returns the single message at id, or nil/empty slice if no
// such message exists. Used by the reclaim loop to recover the original
// payload before routing a stuck entry to the DLQ.
func (q *Queue) RangeOne(ctx context.Context, id string) ([]redis.XMessage, error) {
	return q.rdb.XRange(ctx, StreamQueue, id, id).Result()
}

// ToDLQ moves an entry permanently: appends it to the DLQ stream +
// ACKs + deletes from the main queue. Used when retry count hit the
// cap or the envelope itself is malformed.
func (q *Queue) ToDLQ(ctx context.Context, id string, reason string, values map[string]any) error {
	dlqValues := map[string]any{"reason": reason, "orig_id": id}
	for k, v := range values {
		dlqValues[k] = v
	}
	pipe := q.rdb.TxPipeline()
	pipe.XAdd(ctx, &redis.XAddArgs{Stream: StreamDLQ, MaxLen: maxDLQLen, Approx: true, Values: dlqValues})
	pipe.XAck(ctx, StreamQueue, ConsumerGroup, id)
	pipe.XDel(ctx, StreamQueue, id)
	_, err := pipe.Exec(ctx)
	return err
}
