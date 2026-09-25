package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// GH #1818 / #1834: a distribution group must fan mail out to its members.
// Projecting it as a Stalwart Group account (the M51 shape) parks every
// message in the group's own inbox, which no member can see. The fix projects
// a distribution group as a native x:MailingList, or — when internal_only is
// set — as a Group account whose one active Sieve rejects outside senders and
// redirects to each member. Legacy Group accounts are converted: their stored
// mail is copied to every member first, and the account is only destroyed
// after every copy succeeded.
//
// mgFake is a stateful Stalwart fake covering the registry objects, the mail
// store and the SieveScript store that path touches. It mirrors the live
// behaviours verified on the test box (Stalwart 0.16.15):
//   - an x:MailingList cannot share an address with an existing Account;
//   - x:Account destroy is refused (objectIsLinked) while a member still
//     carries the group in memberGroupIds;
//   - Email/copy of a message the target already holds → alreadyExists.

type mgAccount struct {
	name, domainID, typ string
	memberGroupIDs      map[string]bool
}

type mgList struct {
	name, domainID, description string
	recipients                  map[string]bool
}

type mgFake struct {
	mu       sync.Mutex
	domains  map[string]string // name → id
	accounts map[string]*mgAccount
	lists    map[string]*mgList
	emails   map[string][]string        // account id → email ids
	copied   map[string]map[string]bool // target account id → copied source email ids
	scripts  map[string][]*sieveScriptRec
	blobs    map[string]string
	calls    []string
	failCopy bool
	nextID   int
}

func newMGFake() *mgFake {
	return &mgFake{
		domains:  map[string]string{"example.com": "dom1"},
		accounts: map[string]*mgAccount{},
		lists:    map[string]*mgList{},
		emails:   map[string][]string{},
		copied:   map[string]map[string]bool{},
		scripts:  map[string][]*sieveScriptRec{},
		blobs:    map[string]string{},
	}
}

func (f *mgFake) id(prefix string) string {
	f.nextID++
	return prefix + strconv.Itoa(f.nextID)
}

func (f *mgFake) domainName(id string) string {
	for n, d := range f.domains {
		if d == id {
			return n
		}
	}
	return ""
}

func (f *mgFake) addAccount(email, typ string) string {
	at := strings.LastIndex(email, "@")
	id := f.id("acct")
	f.accounts[id] = &mgAccount{name: email[:at], domainID: f.domains[email[at+1:]], typ: typ, memberGroupIDs: map[string]bool{}}
	return id
}

func (f *mgFake) addList(email string, recipients ...string) string {
	at := strings.LastIndex(email, "@")
	id := f.id("list")
	r := map[string]bool{}
	for _, e := range recipients {
		r[e] = true
	}
	f.lists[id] = &mgList{name: email[:at], domainID: f.domains[email[at+1:]], recipients: r}
	return id
}

func (f *mgFake) accountByEmail(email string) string {
	at := strings.LastIndex(email, "@")
	for id, a := range f.accounts {
		if a.name == email[:at] && a.domainID == f.domains[email[at+1:]] {
			return id
		}
	}
	return ""
}

func (f *mgFake) listsByEmail(email string) []*mgList {
	at := strings.LastIndex(email, "@")
	var out []*mgList
	for _, l := range f.lists {
		if l.name == email[:at] && l.domainID == f.domains[email[at+1:]] {
			out = append(out, l)
		}
	}
	return out
}

func (f *mgFake) activeScript(acct string) *sieveScriptRec {
	for _, s := range f.scripts[acct] {
		if s.active {
			return s
		}
	}
	return nil
}

func (f *mgFake) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, ok := r.BasicAuth(); !ok || u != jmapAdminUser || p == "" {
			http.Error(w, "fake: missing basic auth", http.StatusUnauthorized)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/jmap/upload/") {
			body := readAllString(r)
			f.mu.Lock()
			blobID := f.id("blob")
			f.blobs[blobID] = body
			f.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"blobId": blobID})
			return
		}
		if r.URL.Path != jmapAPIPath {
			http.Error(w, "fake: wrong path "+r.URL.Path, http.StatusNotFound)
			return
		}
		var req jmapRequestBody
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.MethodCalls) != 1 {
			http.Error(w, "fake: bad body", http.StatusBadRequest)
			return
		}
		call := req.MethodCalls[0]
		result, jmapErr := f.dispatch(call.Name, toRaw(call.Args))
		resp := jmapResponseBody{MethodResponses: make([]jmapMethodCall, 1)}
		if jmapErr != nil {
			resp.MethodResponses[0] = jmapMethodCall{Name: "error", Args: jmapErr, CallID: call.CallID}
		} else {
			resp.MethodResponses[0] = jmapMethodCall{Name: call.Name, Args: result, CallID: call.CallID}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newSetResult() jmapSetResult {
	return jmapSetResult{
		Created: map[string]json.RawMessage{}, NotCreated: map[string]json.RawMessage{},
		Updated: map[string]json.RawMessage{}, NotUpdated: map[string]json.RawMessage{},
		NotDestroyed: map[string]json.RawMessage{},
	}
}

func (f *mgFake) dispatch(method string, raw json.RawMessage) (any, *jmapFakeError) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, method)
	var a struct {
		AccountID string                     `json:"accountId"`
		IDs       []string                   `json:"ids"`
		Limit     int                        `json:"limit"`
		Filter    map[string]any             `json:"filter"`
		Create    map[string]json.RawMessage `json:"create"`
		Update    map[string]map[string]any  `json:"update"`
		Destroy   []string                   `json:"destroy"`
		Activate  *string                    `json:"onSuccessActivateScript"`
	}
	_ = json.Unmarshal(raw, &a)
	str := func(k string) string { s, _ := a.Filter[k].(string); return s }

	switch method {
	case "x:Domain/query":
		if id, ok := f.domains[str("name")]; ok {
			return jmapQueryResult{IDs: []string{id}}, nil
		}
		return jmapQueryResult{IDs: []string{}}, nil

	case "x:Account/query":
		ids := []string{}
		for id, acct := range f.accounts {
			if len(a.Filter) == 0 || (acct.name == str("name") && acct.domainID == str("domainId")) {
				ids = append(ids, id)
			}
		}
		return jmapQueryResult{IDs: ids}, nil

	case "x:Account/get":
		list := []json.RawMessage{}
		for _, id := range a.IDs {
			if acct, ok := f.accounts[id]; ok {
				row, _ := json.Marshal(map[string]any{"id": id, "domainId": acct.domainID})
				list = append(list, row)
			}
		}
		return jmapGetResult{List: list}, nil

	case "x:Account/set":
		res := newSetResult()
		for k, rawObj := range a.Create {
			var obj struct {
				Type     string `json:"@type"`
				Name     string `json:"name"`
				DomainID string `json:"domainId"`
			}
			_ = json.Unmarshal(rawObj, &obj)
			email := obj.Name + "@" + f.domainName(obj.DomainID)
			if f.accountByEmail(email) != "" || len(f.listsByEmail(email)) > 0 {
				res.NotCreated[k] = json.RawMessage(`{"type":"primaryKeyViolation"}`)
				continue
			}
			id := f.addAccount(email, obj.Type)
			res.Created[k] = json.RawMessage(`{"id":"` + id + `"}`)
		}
		for id, patch := range a.Update {
			acct, ok := f.accounts[id]
			if !ok {
				res.NotUpdated[id] = json.RawMessage(`{"type":"notFound"}`)
				continue
			}
			for k, v := range patch {
				if gid, ok := strings.CutPrefix(k, "memberGroupIds/"); ok {
					if v == nil {
						delete(acct.memberGroupIDs, gid)
					} else {
						acct.memberGroupIDs[gid] = true
					}
				}
			}
			res.Updated[id] = json.RawMessage(`null`)
		}
		for _, id := range a.Destroy {
			if _, ok := f.accounts[id]; !ok {
				res.NotDestroyed[id] = json.RawMessage(`{"type":"notFound"}`)
				continue
			}
			linked := false
			for _, other := range f.accounts {
				if other.memberGroupIDs[id] {
					linked = true
				}
			}
			if linked {
				res.NotDestroyed[id] = json.RawMessage(`{"type":"objectIsLinked"}`)
				continue
			}
			delete(f.accounts, id)
			delete(f.emails, id)
			res.Destroyed = append(res.Destroyed, id)
		}
		return res, nil

	case "x:MailingList/query":
		ids := []string{}
		for id, l := range f.lists {
			if len(a.Filter) == 0 || strings.EqualFold(l.name, str("text")) {
				ids = append(ids, id)
			}
		}
		return jmapQueryResult{IDs: ids}, nil

	case "x:MailingList/get":
		list := []json.RawMessage{}
		for _, id := range a.IDs {
			l, ok := f.lists[id]
			if !ok {
				continue
			}
			row, _ := json.Marshal(map[string]any{
				"id": id, "name": l.name, "domainId": l.domainID,
				"emailAddress": l.name + "@" + f.domainName(l.domainID),
				"recipients":   l.recipients, "description": l.description,
			})
			list = append(list, row)
		}
		return jmapGetResult{List: list}, nil

	case "x:MailingList/set":
		res := newSetResult()
		for k, rawObj := range a.Create {
			var obj struct {
				Name        string          `json:"name"`
				DomainID    string          `json:"domainId"`
				Description string          `json:"description"`
				Recipients  map[string]bool `json:"recipients"`
			}
			_ = json.Unmarshal(rawObj, &obj)
			email := obj.Name + "@" + f.domainName(obj.DomainID)
			if f.accountByEmail(email) != "" || len(f.listsByEmail(email)) > 0 {
				res.NotCreated[k] = json.RawMessage(`{"type":"alreadyExists"}`)
				continue
			}
			id := f.id("list")
			f.lists[id] = &mgList{name: obj.Name, domainID: obj.DomainID, description: obj.Description, recipients: obj.Recipients}
			res.Created[k] = json.RawMessage(`{"id":"` + id + `"}`)
		}
		for id, patch := range a.Update {
			l, ok := f.lists[id]
			if !ok {
				res.NotUpdated[id] = json.RawMessage(`{"type":"notFound"}`)
				continue
			}
			if rv, ok := patch["recipients"].(map[string]any); ok {
				l.recipients = map[string]bool{}
				for e := range rv {
					l.recipients[e] = true
				}
			}
			if d, ok := patch["description"].(string); ok {
				l.description = d
			}
			res.Updated[id] = json.RawMessage(`null`)
		}
		for _, id := range a.Destroy {
			if _, ok := f.lists[id]; !ok {
				res.NotDestroyed[id] = json.RawMessage(`{"type":"notFound"}`)
				continue
			}
			delete(f.lists, id)
			res.Destroyed = append(res.Destroyed, id)
		}
		return res, nil

	case "Email/query":
		ids := append([]string{}, f.emails[a.AccountID]...)
		total := len(ids)
		if a.Limit > 0 && len(ids) > a.Limit {
			ids = ids[:a.Limit]
		}
		return map[string]any{"ids": ids, "total": total}, nil

	case "Mailbox/query":
		if _, ok := f.accounts[a.AccountID]; !ok {
			return nil, &jmapFakeError{Type: "accountNotFound"}
		}
		return jmapQueryResult{IDs: []string{"inbox-" + a.AccountID}}, nil

	case "Email/copy":
		res := newSetResult()
		for k, rawObj := range a.Create {
			var obj struct {
				ID         string          `json:"id"`
				MailboxIDs map[string]bool `json:"mailboxIds"`
			}
			_ = json.Unmarshal(rawObj, &obj)
			if f.failCopy {
				res.NotCreated[k] = json.RawMessage(`{"type":"serverFail"}`)
				continue
			}
			if !obj.MailboxIDs["inbox-"+a.AccountID] {
				res.NotCreated[k] = json.RawMessage(`{"type":"invalidProperties"}`)
				continue
			}
			if f.copied[a.AccountID] == nil {
				f.copied[a.AccountID] = map[string]bool{}
			}
			if f.copied[a.AccountID][obj.ID] {
				res.NotCreated[k] = json.RawMessage(`{"type":"alreadyExists"}`)
				continue
			}
			f.copied[a.AccountID][obj.ID] = true
			res.Created[k] = json.RawMessage(`{"id":"` + f.id("copy") + `"}`)
		}
		return res, nil

	case "Email/set":
		res := newSetResult()
		keep := []string{}
		drop := map[string]bool{}
		for _, id := range a.Destroy {
			drop[id] = true
		}
		for _, id := range f.emails[a.AccountID] {
			if drop[id] {
				res.Destroyed = append(res.Destroyed, id)
			} else {
				keep = append(keep, id)
			}
		}
		f.emails[a.AccountID] = keep
		return res, nil

	case "SieveScript/get":
		list := []json.RawMessage{}
		for _, s := range f.scripts[a.AccountID] {
			row, _ := json.Marshal(map[string]any{"id": s.id, "name": s.name, "isActive": s.active})
			list = append(list, row)
		}
		return jmapGetResult{List: list}, nil

	case "SieveScript/set":
		res := newSetResult()
		created := map[string]string{}
		for k, rawObj := range a.Create {
			var obj struct {
				Name   string `json:"name"`
				BlobID string `json:"blobId"`
			}
			_ = json.Unmarshal(rawObj, &obj)
			dup := false
			for _, s := range f.scripts[a.AccountID] {
				if s.name == obj.Name {
					dup = true
				}
			}
			if dup {
				res.NotCreated[k] = json.RawMessage(`{"type":"alreadyExists"}`)
				continue
			}
			id := f.id("sieve")
			f.scripts[a.AccountID] = append(f.scripts[a.AccountID], &sieveScriptRec{id: id, name: obj.Name, blobID: obj.BlobID})
			created["#"+k] = id
			res.Created[k] = json.RawMessage(`{"id":"` + id + `"}`)
		}
		for id, patch := range a.Update {
			var found *sieveScriptRec
			for _, s := range f.scripts[a.AccountID] {
				if s.id == id {
					found = s
				}
			}
			if found == nil {
				res.NotUpdated[id] = json.RawMessage(`{"type":"notFound"}`)
				continue
			}
			if b, ok := patch["blobId"].(string); ok {
				found.blobID = b
			}
			if act, ok := patch["isActive"].(bool); ok {
				found.active = act
			}
			res.Updated[id] = json.RawMessage(`null`)
		}
		for _, id := range a.Destroy {
			kept := f.scripts[a.AccountID][:0]
			for _, s := range f.scripts[a.AccountID] {
				if s.id == id {
					res.Destroyed = append(res.Destroyed, id)
					continue
				}
				kept = append(kept, s)
			}
			f.scripts[a.AccountID] = kept
		}
		if a.Activate != nil {
			target := *a.Activate
			if id, ok := created[target]; ok {
				target = id
			}
			for _, s := range f.scripts[a.AccountID] {
				s.active = s.id == target
			}
		}
		return res, nil
	}
	// Best-effort side calls (identity rename, calendar rename, …) are not
	// part of the distribution path; answer them with an empty result so a
	// stray call is visible in f.calls without failing the apply.
	return map[string]any{"list": []any{}, "ids": []any{}}, nil
}

// calledBefore reports whether the first call of method `first` precedes the
// first call of method `second`.
func (f *mgFake) calledBefore(first, second string) bool {
	fi, si := -1, -1
	for i, c := range f.calls {
		if c == first && fi == -1 {
			fi = i
		}
		if c == second && si == -1 {
			si = i
		}
	}
	return fi != -1 && si != -1 && fi < si
}

func applyDistribution(t *testing.T, email string, internalOnly bool, members ...string) error {
	t.Helper()
	if members == nil {
		members = []string{}
	}
	params, _ := json.Marshal(map[string]any{
		"email":         email,
		"display_name":  "Sales",
		"group_kind":    "distribution",
		"internal_only": internalOnly,
		"member_emails": members,
	})
	_, err := mailGroupApplyHandler(context.Background(), params)
	return err
}

func recipientsOf(l *mgList) []string {
	out := make([]string, 0, len(l.recipients))
	for e := range l.recipients {
		out = append(out, e)
	}
	sort.Strings(out)
	return out
}

func TestMailGroupApplyDistribution_CreatesMailingList(t *testing.T) {
	f := newMGFake()
	f.addAccount("alice@example.com", "User")
	f.addAccount("bob@example.com", "User")
	wireJMAP(t, f.server(t))

	if err := applyDistribution(t, "sales@example.com", false, "alice@example.com", "Bob@Example.com"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	lists := f.listsByEmail("sales@example.com")
	if len(lists) != 1 {
		t.Fatalf("want exactly one MailingList for sales@, got %d", len(lists))
	}
	if got := recipientsOf(lists[0]); fmt.Sprint(got) != "[alice@example.com bob@example.com]" {
		t.Fatalf("recipients = %v", got)
	}
	if lists[0].description != "Sales" {
		t.Fatalf("list description = %q, want the display name", lists[0].description)
	}
	if f.accountByEmail("sales@example.com") != "" {
		t.Fatalf("a distribution group must not be projected as a Group account")
	}
}

func TestMailGroupApplyDistribution_ReplacesRecipients(t *testing.T) {
	f := newMGFake()
	f.addList("sales@example.com", "alice@example.com", "bob@example.com")
	wireJMAP(t, f.server(t))

	if err := applyDistribution(t, "sales@example.com", false, "bob@example.com", "carol@example.com"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	lists := f.listsByEmail("sales@example.com")
	if len(lists) != 1 {
		t.Fatalf("want exactly one MailingList, got %d", len(lists))
	}
	if got := recipientsOf(lists[0]); fmt.Sprint(got) != "[bob@example.com carol@example.com]" {
		t.Fatalf("recipients = %v (removed members must drop off)", got)
	}
}

func TestMailGroupApplyDistribution_ConvertsLegacyGroupAccount(t *testing.T) {
	f := newMGFake()
	gid := f.addAccount("sales@example.com", "Group")
	f.emails[gid] = []string{"m1", "m2"}
	alice := f.addAccount("alice@example.com", "User")
	bob := f.addAccount("bob@example.com", "User")
	f.accounts[bob].memberGroupIDs[gid] = true // stray edge must not block the destroy
	wireJMAP(t, f.server(t))

	if err := applyDistribution(t, "sales@example.com", false, "alice@example.com", "bob@example.com"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	for _, m := range []string{alice, bob} {
		if !f.copied[m]["m1"] || !f.copied[m]["m2"] {
			t.Fatalf("member %s did not receive the stored group mail: %v", m, f.copied[m])
		}
	}
	if _, ok := f.accounts[gid]; ok {
		t.Fatalf("legacy Group account should be destroyed after its mail was copied")
	}
	if len(f.listsByEmail("sales@example.com")) != 1 {
		t.Fatalf("MailingList not created after conversion")
	}
	if !f.calledBefore("Email/copy", "x:MailingList/set") {
		t.Fatalf("mail must be copied before the list takes over the address; calls=%v", f.calls)
	}
}

func TestMailGroupApplyDistribution_CopyFailureKeepsGroupAccount(t *testing.T) {
	f := newMGFake()
	f.failCopy = true
	gid := f.addAccount("sales@example.com", "Group")
	f.emails[gid] = []string{"m1"}
	f.addAccount("alice@example.com", "User")
	wireJMAP(t, f.server(t))

	if err := applyDistribution(t, "sales@example.com", false, "alice@example.com"); err == nil {
		t.Fatalf("a failed copy must fail the apply")
	}
	if _, ok := f.accounts[gid]; !ok {
		t.Fatalf("Group account destroyed although its mail was not copied — data loss")
	}
	if len(f.emails[gid]) != 1 {
		t.Fatalf("source mail removed although the copy failed: %v", f.emails[gid])
	}
	if len(f.listsByEmail("sales@example.com")) != 0 {
		t.Fatalf("MailingList created while the Group account still holds the address")
	}
}

func TestMailGroupApplyDistribution_LegacyMailWithoutMembersIsKept(t *testing.T) {
	f := newMGFake()
	gid := f.addAccount("sales@example.com", "Group")
	f.emails[gid] = []string{"m1"}
	wireJMAP(t, f.server(t))

	err := applyDistribution(t, "sales@example.com", false)
	requireAgentErrorCode(t, err, agentwire.CodeFailedPrecondition)
	if _, ok := f.accounts[gid]; !ok {
		t.Fatalf("Group account holding mail destroyed with no member to copy it to")
	}
}

func TestMailGroupApplyDistribution_NoMembersProjectsNothing(t *testing.T) {
	f := newMGFake()
	f.addList("sales@example.com", "alice@example.com")
	gid := f.addAccount("old@example.com", "Group") // unrelated, must survive
	wireJMAP(t, f.server(t))

	if err := applyDistribution(t, "sales@example.com", false); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if n := len(f.listsByEmail("sales@example.com")); n != 0 {
		t.Fatalf("a member-less distribution list must not accept mail; %d list(s) remain", n)
	}
	if _, ok := f.accounts[gid]; !ok {
		t.Fatalf("unrelated account touched")
	}
}

func TestMailGroupApplyDistribution_InternalOnlyRedirectsViaSieve(t *testing.T) {
	f := newMGFake()
	f.addList("sales@example.com", "alice@example.com") // toggled from a normal list
	f.addAccount("alice@example.com", "User")
	f.addAccount("bob@example.com", "User")
	wireJMAP(t, f.server(t))

	if err := applyDistribution(t, "sales@example.com", true, "alice@example.com", "bob@example.com"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if n := len(f.listsByEmail("sales@example.com")); n != 0 {
		t.Fatalf("internal-only list must replace the MailingList, %d remain", n)
	}
	gid := f.accountByEmail("sales@example.com")
	if gid == "" {
		t.Fatalf("internal-only distribution list needs a Group account to run its Sieve")
	}
	act := f.activeScript(gid)
	if act == nil {
		t.Fatalf("no active Sieve on the group account")
	}
	script := f.blobs[act.blobID]
	for _, want := range []string{
		`if not envelope :domain :is "from" "example.com"`,
		`reject "550 5.7.1`,
		"stop;",
		`redirect "alice@example.com";`,
		`redirect "bob@example.com";`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("script missing %q:\n%s", want, script)
		}
	}
	if strings.Index(script, "stop;") > strings.Index(script, "redirect") {
		t.Fatalf("an outside sender must stop before any redirect:\n%s", script)
	}
	if len(f.scripts[gid]) != 1 {
		t.Fatalf("want one script on the group account, got %d", len(f.scripts[gid]))
	}

	// Re-apply with a changed member set: the SAME script is updated in place.
	if err := applyDistribution(t, "sales@example.com", true, "bob@example.com"); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
	if len(f.scripts[gid]) != 1 {
		t.Fatalf("re-apply created a second script: %d", len(f.scripts[gid]))
	}
	script = f.blobs[f.activeScript(gid).blobID]
	if strings.Contains(script, "alice@example.com") || !strings.Contains(script, `redirect "bob@example.com";`) {
		t.Fatalf("re-apply did not rewrite the redirect set:\n%s", script)
	}
}

func TestMailGroupApplyDistribution_InternalOnlyOffConvertsToList(t *testing.T) {
	f := newMGFake()
	gid := f.addAccount("sales@example.com", "Group")
	f.addAccount("alice@example.com", "User")
	wireJMAP(t, f.server(t))

	if err := applyDistribution(t, "sales@example.com", false, "alice@example.com"); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, ok := f.accounts[gid]; ok {
		t.Fatalf("Group account must be removed when internal-only is switched off")
	}
	if len(f.listsByEmail("sales@example.com")) != 1 {
		t.Fatalf("MailingList not created")
	}
}

func TestMailGroupApplyDistribution_InternalOnlyMemberCap(t *testing.T) {
	f := newMGFake()
	wireJMAP(t, f.server(t))

	members := make([]string, 0, maxInternalOnlyListMembers+1)
	for i := 0; i <= maxInternalOnlyListMembers; i++ {
		members = append(members, fmt.Sprintf("m%d@example.com", i))
	}
	err := applyDistribution(t, "sales@example.com", true, members...)
	requireAgentErrorCode(t, err, agentwire.CodeInvalidArgument)
	if f.accountByEmail("sales@example.com") != "" || len(f.listsByEmail("sales@example.com")) != 0 {
		t.Fatalf("over-cap apply must not project anything")
	}
}

func TestMailGroupApplyDistribution_RejectsUnsafeMemberAddress(t *testing.T) {
	f := newMGFake()
	wireJMAP(t, f.server(t))

	err := applyDistribution(t, "sales@example.com", true, `x";discard;"@example.com`)
	requireAgentErrorCode(t, err, agentwire.CodeInvalidArgument)
	if len(f.calls) != 0 {
		t.Fatalf("an unsafe member address must be rejected before any mail-server call: %v", f.calls)
	}
}

func TestMailGroupApply_UnknownKindRejected(t *testing.T) {
	_, err := mailGroupApplyHandler(context.Background(),
		json.RawMessage(`{"email":"sales@example.com","group_kind":"bogus"}`))
	requireAgentErrorCode(t, err, agentwire.CodeInvalidArgument)
}

func TestMailGroupDelete_DestroysMailingList(t *testing.T) {
	f := newMGFake()
	f.addList("sales@example.com", "alice@example.com")
	other := f.addList("other@example.com", "alice@example.com")
	wireJMAP(t, f.server(t))

	if _, err := mailGroupDeleteHandler(context.Background(),
		json.RawMessage(`{"group_email":"sales@example.com","member_emails":["alice@example.com"]}`)); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if len(f.listsByEmail("sales@example.com")) != 0 {
		t.Fatalf("MailingList survived group delete")
	}
	if _, ok := f.lists[other]; !ok {
		t.Fatalf("delete removed an unrelated list")
	}
}

// Deleting a domain (or its owner) purges every registry object under it. A
// distribution list left behind would keep its address alive: mail to it
// would still be accepted and fanned out, and a later tenant could not create
// a mailbox with that address (GH #1818).
func TestMailDomainPurge_DestroysMailingLists(t *testing.T) {
	f := newMGFake()
	f.domains["other.com"] = "dom2"
	alice := f.addAccount("alice@example.com", "User")
	f.addList("sales@example.com", "alice@example.com")
	keep := f.addList("sales@other.com", "bob@other.com")
	keepAcct := f.addAccount("bob@other.com", "User")
	wireJMAP(t, f.server(t))

	if _, err := mailDomainPurgeHandler(context.Background(), json.RawMessage(`{"domain":"example.com"}`)); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if _, ok := f.accounts[alice]; ok {
		t.Fatalf("account in the purged domain survived")
	}
	if len(f.listsByEmail("sales@example.com")) != 0 {
		t.Fatalf("mailing list in the purged domain survived")
	}
	if _, ok := f.lists[keep]; !ok {
		t.Fatalf("purge removed a list in another domain")
	}
	if _, ok := f.accounts[keepAcct]; !ok {
		t.Fatalf("purge removed an account in another domain")
	}
}
