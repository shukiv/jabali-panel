package commands

import (
	"context"
	"encoding/json"
	"testing"
)

// createDomain must work on Stalwart 0.16 and 1.0. The enabled flag is named
// isEnabled on 0.16 and enable on 1.0, and 1.0 refuses isEnabled with
// invalidPatch "Invalid property" (seen on 1.0.2). Both create the domain
// enabled when the flag is missing, so createDomain sends neither name.
func TestCreateDomain_SendsNoEnabledFlag(t *testing.T) {
	var sent map[string]json.RawMessage
	srv := newJMAPServer(t, map[string]jmapHandler{
		"x:Domain/set": func(args json.RawMessage) (any, *jmapFakeError) {
			var a struct {
				Create map[string]map[string]json.RawMessage `json:"create"`
			}
			if err := json.Unmarshal(args, &a); err != nil {
				return nil, &jmapFakeError{Type: "invalidArguments", Description: err.Error()}
			}
			sent = a.Create["#d1"]
			// What Stalwart 1.0 answers to a property it does not know.
			for _, bad := range []string{"isEnabled", "enable"} {
				if _, ok := sent[bad]; ok {
					return map[string]any{"notCreated": map[string]any{
						"#d1": map[string]any{"type": "invalidPatch", "description": "Invalid property", "properties": []string{bad}},
					}}, nil
				}
			}
			return map[string]any{"created": map[string]any{"#d1": map[string]any{"id": "d9"}}}, nil
		},
	})
	defer srv.Close()
	wireJMAP(t, srv)

	id, err := createDomain(context.Background(), "example.com")
	if err != nil {
		t.Fatalf("createDomain: %v", err)
	}
	if id != "d9" {
		t.Errorf("id = %q, want d9", id)
	}
	if string(sent["name"]) != `"example.com"` {
		t.Errorf("name = %s, want \"example.com\"", sent["name"])
	}
	for _, k := range []string{"dkimManagement", "dnsManagement", "certificateManagement", "subAddressing"} {
		if _, ok := sent[k]; !ok {
			t.Errorf("create payload lost %s", k)
		}
	}
}
