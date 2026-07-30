package notifications

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestPublish_SelfHealsMissingGroup is the notifications-NOGROUP regression:
// after Redis loses the stream/group (restart without persistence, flush,
// maxmemory eviction), a Publish must recreate the group so the message is
// consumable — instead of the dispatcher's XReadGroup NOGROUP-ing forever.
func TestPublish_SelfHealsMissingGroup(t *testing.T) {
	rdb, mr := newRedis(t)
	q := NewQueue(rdb)
	ctx := context.Background()

	require.NoError(t, q.EnsureGroup(ctx))
	// Simulate a Redis wipe: drop the whole stream key (takes the group with it).
	mr.Del(StreamQueue)

	// Publish must self-heal the group before appending.
	id, err := q.Publish(ctx, Envelope{EventKind: "test.event"})
	require.NoError(t, err)
	require.NotEmpty(t, id)

	// The dispatcher can now read the just-published message via the group.
	streams, err := q.Read(ctx, "consumer-1", 10, 50*time.Millisecond)
	require.NoError(t, err)
	var count int
	for _, s := range streams {
		count += len(s.Messages)
	}
	require.Equal(t, 1, count, "message published after a wipe must be consumable")
}

// TestRead_RecoversFromNOGROUP: if the group vanishes while the consumer loops,
// Read recreates it and returns "no work" instead of erroring.
func TestRead_RecoversFromNOGROUP(t *testing.T) {
	rdb, mr := newRedis(t)
	q := NewQueue(rdb)
	ctx := context.Background()

	require.NoError(t, q.EnsureGroup(ctx))
	mr.Del(StreamQueue) // group gone

	streams, err := q.Read(ctx, "consumer-1", 10, 10*time.Millisecond)
	require.NoError(t, err, "NOGROUP must be recovered, not surfaced")
	require.Empty(t, streams)

	// Group is back: a subsequent publish + read works.
	_, err = q.Publish(ctx, Envelope{EventKind: "test.event"})
	require.NoError(t, err)
	streams, err = q.Read(ctx, "consumer-1", 10, 50*time.Millisecond)
	require.NoError(t, err)
	var count int
	for _, s := range streams {
		count += len(s.Messages)
	}
	require.Equal(t, 1, count)
}
