package commands

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
)

// Stalwart 1.0 made delivery.completed a debug event, so the delivery tracer
// must run at debug (its include-only event list keeps the log to its four
// events). ensureDeliveryTracer creates it at debug and raises an existing one.

const fullDeliveryEvents = `{"queue.message-queued":true,"queue.authenticated-message-queued":true,"delivery.completed":true,"delivery.failed":true}`

func tracerServer(t *testing.T, existing string, sets *[]json.RawMessage, reloads *int) {
	t.Helper()
	routes := map[string]jmapHandler{
		"x:Tracer/set": func(args json.RawMessage) (any, *jmapFakeError) {
			*sets = append(*sets, args)
			return map[string]any{"created": map[string]any{"jabali-delivery-log": map[string]any{"id": "t9"}}, "updated": map[string]any{"t1": nil}}, nil
		},
		"x:Action/set": func(json.RawMessage) (any, *jmapFakeError) {
			*reloads++
			return map[string]any{"created": map[string]any{"reload": map[string]any{"id": "r1"}}}, nil
		},
	}
	if existing == "" {
		routes["x:Tracer/query"] = jmapHandlerReturning(jmapQueryResult{})
	} else {
		routes["x:Tracer/query"] = jmapHandlerReturning(jmapQueryResult{IDs: []string{"t1"}, Total: 1})
		routes["x:Tracer/get"] = jmapHandlerReturning(jmapGetResult{List: []json.RawMessage{json.RawMessage(existing)}})
	}
	srv := newJMAPServer(t, routes)
	t.Cleanup(srv.Close)
	wireJMAP(t, srv)
	deliveryTracerBackfillOnce = sync.Once{}
}

func setLevel(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var a struct {
		Create map[string]map[string]any `json:"create"`
		Update map[string]map[string]any `json:"update"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatalf("set args: %v", err)
	}
	for _, m := range []map[string]map[string]any{a.Create, a.Update} {
		for _, v := range m {
			s, _ := v["level"].(string)
			return s
		}
	}
	return ""
}

func TestEnsureDeliveryTracer_CreatesAtDebug(t *testing.T) {
	var sets []json.RawMessage
	var reloads int
	tracerServer(t, "", &sets, &reloads)
	if err := ensureDeliveryTracer(context.Background()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if len(sets) != 1 || setLevel(t, sets[0]) != "debug" {
		t.Fatalf("sets=%s, want one create at level debug", sets)
	}
	if reloads != 1 {
		t.Fatalf("reloads=%d, want 1", reloads)
	}
}

func TestEnsureDeliveryTracer_RaisesAnInfoTracerToDebug(t *testing.T) {
	var sets []json.RawMessage
	var reloads int
	tracerServer(t, `{"id":"t1","prefix":"delivery","level":"info","events":`+fullDeliveryEvents+`}`, &sets, &reloads)
	if err := ensureDeliveryTracer(context.Background()); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	if len(sets) != 1 || setLevel(t, sets[0]) != "debug" {
		t.Fatalf("sets=%s, want one update to level debug", sets)
	}
	if reloads != 1 {
		t.Fatalf("reloads=%d, want 1", reloads)
	}
}

func TestEnsureDeliveryTracer_LeavesADebugTracerAlone(t *testing.T) {
	for _, level := range []string{"debug", "trace"} {
		t.Run(level, func(t *testing.T) {
			var sets []json.RawMessage
			var reloads int
			tracerServer(t, `{"id":"t1","prefix":"delivery","level":"`+level+`","events":`+fullDeliveryEvents+`}`, &sets, &reloads)
			if err := ensureDeliveryTracer(context.Background()); err != nil {
				t.Fatalf("ensure: %v", err)
			}
			if len(sets) != 0 || reloads != 0 {
				t.Fatalf("sets=%s reloads=%d, want nothing written", sets, reloads)
			}
		})
	}
}
