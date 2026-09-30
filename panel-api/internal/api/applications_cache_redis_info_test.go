package api

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/redis/go-redis/v9"
)

// infoHook answers INFO without a server: with a reply, or with an error.
type infoHook struct {
	reply string
	err   error
}

func (h infoHook) DialHook(next redis.DialHook) redis.DialHook { return next }

func (h infoHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if cmd.Name() != "info" {
			return next(ctx, cmd)
		}
		if h.err != nil {
			cmd.SetErr(h.err)
			return h.err
		}
		cmd.(*redis.StringCmd).SetVal(h.reply)
		return nil
	}
}

func (h infoHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func infoClient(t *testing.T, h infoHook) *redis.Client {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1"})
	rdb.AddHook(h)
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

func captureDefaultLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// The panel's Redis user was refused INFO (NOPERM) and the error was dropped,
// so the eviction warning never showed. A refused INFO must now be logged.
func TestRedisServerInfo_LogsARefusedInfo(t *testing.T) {
	logs := captureDefaultLog(t)
	rdb := infoClient(t, infoHook{err: errors.New("NOPERM User jabali_panel has no permissions to run the 'info' command")})

	info, ok := redisServerInfo(context.Background(), rdb, "stats")
	if ok || info != "" {
		t.Fatalf("got (%q, %v), want a failure", info, ok)
	}
	if !strings.Contains(logs.String(), "redis INFO failed") || !strings.Contains(logs.String(), "NOPERM") {
		t.Fatalf("a refused INFO must be logged with its error, got log %q", logs.String())
	}
}

func TestRedisServerInfo_ReturnsTheReply(t *testing.T) {
	logs := captureDefaultLog(t)
	rdb := infoClient(t, infoHook{reply: "# Stats\r\nevicted_keys:3\r\n"})

	info, ok := redisServerInfo(context.Background(), rdb, "stats")
	if !ok || infoInt(info, "evicted_keys") != 3 {
		t.Fatalf("got (%q, %v), want evicted_keys 3", info, ok)
	}
	if logs.Len() != 0 {
		t.Fatalf("a successful INFO must not log, got %q", logs.String())
	}
}
