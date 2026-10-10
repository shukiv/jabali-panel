package backup

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// The key a mailbox's trusted senders are stored under is read back by
// every later restore, so it can't change (GH #2017).
func TestMetadataMailbox_TrustedSendersKey(t *testing.T) {
	var mb MetadataMailbox
	if err := json.Unmarshal([]byte(`{"id":"m1","local_part":"info","trusted_senders":["a@x.com","b@y.org"]}`), &mb); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(mb.TrustedSenders, []string{"a@x.com", "b@y.org"}) {
		t.Fatalf("TrustedSenders = %v", mb.TrustedSenders)
	}

	raw, err := json.Marshal(MetadataMailbox{ID: "m1", LocalPart: "info"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "trusted") {
		t.Fatalf("a mailbox with no trusted senders wrote %s", raw)
	}
}
