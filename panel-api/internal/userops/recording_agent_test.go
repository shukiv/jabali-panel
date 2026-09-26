package userops

import (
	"context"
	"encoding/json"
)

// recordingAgent records every agent call and returns retErr for all of them.
type recordingAgent struct {
	calls  []recordedCall
	retErr error
}

type recordedCall struct {
	method string
	params any
}

func (r *recordingAgent) Call(_ context.Context, method string, params any) (json.RawMessage, error) {
	r.calls = append(r.calls, recordedCall{method: method, params: params})
	return json.RawMessage(`{}`), r.retErr
}
