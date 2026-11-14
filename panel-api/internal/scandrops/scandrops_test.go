package scandrops

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ftpops"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

type fakeDrops struct {
	rows      map[string]models.ScanDrop
	createErr error
	recorded  []string
}

func (f *fakeDrops) FindByResourceID(_ context.Context, id string) (*models.ScanDrop, error) {
	r, ok := f.rows[id]
	if !ok {
		return nil, repository.ErrNotFound
	}
	return &r, nil
}

func (f *fakeDrops) ListAll(context.Context) ([]models.ScanDrop, error) {
	var out []models.ScanDrop
	for _, r := range f.rows {
		out = append(out, r)
	}
	return out, nil
}

func (f *fakeDrops) Create(_ context.Context, d *models.ScanDrop) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.rows[d.ResourceID] = *d
	return nil
}

func (f *fakeDrops) Delete(_ context.Context, id string) error {
	if _, ok := f.rows[id]; !ok {
		return repository.ErrNotFound
	}
	delete(f.rows, id)
	return nil
}

func (f *fakeDrops) RecordIngest(_ context.Context, id string, moved uint64, lastError string) error {
	r := f.rows[id]
	r.IngestedTotal += moved
	r.LastError = lastError
	f.rows[id] = r
	f.recorded = append(f.recorded, lastError)
	return nil
}

type fakeResources map[string]*models.SharedResource

func (f fakeResources) FindByID(_ context.Context, id string) (*models.SharedResource, error) {
	if r, ok := f[id]; ok {
		return r, nil
	}
	return nil, repository.ErrNotFound
}

type fakeDomains map[string]*models.Domain

func (f fakeDomains) FindByID(_ context.Context, id string) (*models.Domain, error) {
	if d, ok := f[id]; ok {
		return d, nil
	}
	return nil, repository.ErrNotFound
}

type fakeAccounts struct {
	repository.FtpAccountRepository
	rows map[string]models.FtpAccount
}

func (f *fakeAccounts) ReserveWithinCap(_ context.Context, a *models.FtpAccount, maxAccounts int, _ uint32) error {
	n := 0
	for _, r := range f.rows {
		if r.UserID == a.UserID {
			n++
		}
	}
	if n >= maxAccounts {
		return repository.ErrFtpCapExceeded
	}
	f.rows[a.ID] = *a
	return nil
}

func (f *fakeAccounts) FindByID(_ context.Context, id string) (*models.FtpAccount, error) {
	if r, ok := f.rows[id]; ok {
		return &r, nil
	}
	return nil, repository.ErrNotFound
}

func (f *fakeAccounts) Delete(_ context.Context, id string) error {
	delete(f.rows, id)
	return nil
}

func (f *fakeAccounts) List(context.Context) ([]models.FtpAccount, error) { return nil, nil }

func (f *fakeAccounts) AllocateUID(context.Context) (uint32, error) { return 1000000007, nil }

type fakeUsers struct {
	repository.UserRepository
	users map[string]*models.User
}

func (f fakeUsers) FindByID(_ context.Context, id string) (*models.User, error) {
	if u, ok := f.users[id]; ok {
		return u, nil
	}
	return nil, repository.ErrNotFound
}

type fakePackages map[string]*models.HostingPackage

func (f fakePackages) FindByID(_ context.Context, id string) (*models.HostingPackage, error) {
	if p, ok := f[id]; ok {
		return p, nil
	}
	return nil, repository.ErrNotFound
}

type agentCall struct {
	command string
	params  map[string]any
}

type fakeAgent struct {
	calls     []agentCall
	isolation bool
	ingest    agentwire.ScanDropIngestResult
	errOn     map[string]error
}

func (a *fakeAgent) Call(_ context.Context, command string, params any) (json.RawMessage, error) {
	raw, _ := json.Marshal(params)
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	a.calls = append(a.calls, agentCall{command, m})
	if err := a.errOn[command]; err != nil {
		return nil, err
	}
	switch command {
	case "ftpaccount.isolation_status":
		return json.Marshal(map[string]bool{"available": a.isolation})
	case "scandrop.ingest":
		return json.Marshal(a.ingest)
	}
	return json.RawMessage(`{}`), nil
}

func (a *fakeAgent) commands() []string {
	var out []string
	for _, c := range a.calls {
		out = append(out, c.command)
	}
	return out
}

func (a *fakeAgent) find(command string) map[string]any {
	for _, c := range a.calls {
		if c.command == command {
			return c.params
		}
	}
	return nil
}

type world struct {
	d        Deps
	drops    *fakeDrops
	res      fakeResources
	accounts *fakeAccounts
	agent    *fakeAgent
	pkg      *models.HostingPackage
	owner    *models.User
}

func strp(s string) *string { return &s }

func newWorld() *world {
	pkg := &models.HostingPackage{ID: "pkg1", MaxFTPAccounts: 2, DiskQuotaMB: 5000}
	owner := &models.User{ID: "u1", Username: strp("alice"), PackageID: strp("pkg1")}
	w := &world{
		drops:    &fakeDrops{rows: map[string]models.ScanDrop{}},
		res:      fakeResources{"r1": {ID: "r1", DomainID: "d1", Kind: "files", LocalPart: strp("scans"), EmailCached: strp("scans@example.com"), CollectionID: "b"}},
		accounts: &fakeAccounts{rows: map[string]models.FtpAccount{}},
		agent:    &fakeAgent{errOn: map[string]error{}},
		pkg:      pkg,
		owner:    owner,
	}
	w.d = Deps{
		Drops:     w.drops,
		Resources: w.res,
		Domains:   fakeDomains{"d1": {ID: "d1", Name: "example.com", UserID: "u1"}},
		Ftp: ftpops.Deps{
			Agent:      w.agent,
			Accounts:   w.accounts,
			Users:      fakeUsers{users: map[string]*models.User{"u1": owner}},
			Packages:   fakePackages{"pkg1": pkg},
			QuotaMount: "/",
		},
	}
	return w
}

func (w *world) enable(t *testing.T, maxMB uint32) (*models.ScanDrop, *models.FtpAccount, error) {
	t.Helper()
	dom, _ := w.d.Domains.FindByID(context.Background(), "d1")
	return Enable(context.Background(), w.d, EnableInput{
		Resource: w.res["r1"], Domain: dom, Label: "scanner", Password: "a-long-enough-password", MaxMB: maxMB,
	})
}

func TestEnable_MakesTheFolderThenTheLoginThenTheRow(t *testing.T) {
	w := newWorld()
	row, acct, err := w.enable(t, 1024)
	if err != nil {
		t.Fatalf("Enable: %v", err)
	}
	const drop = "/home/alice/domains/example.com/scans/scans"
	cmds := strings.Join(w.agent.commands(), " ")
	if !strings.HasPrefix(cmds, "scandrop.prepare ftpaccount.isolation_status ftpaccount.create") {
		t.Fatalf("agent calls = %s, want prepare, then the isolation probe, then the login", cmds)
	}
	if got := w.agent.find("scandrop.prepare")["drop_path"]; got != drop {
		t.Fatalf("prepare drop_path = %v, want %s", got, drop)
	}
	create := w.agent.find("ftpaccount.create")
	if create["home_path"] != drop || create["username"] != "alice_scanner" || create["ftp_access"] != true {
		t.Fatalf("ftpaccount.create = %v, want alice_scanner homed at the drop folder with FTP on", create)
	}
	if _, isolated := create["isolated"]; isolated {
		t.Fatalf("ftpaccount.create = %v: isolated although the probe said no", create)
	}
	if row.FtpAccountID != acct.ID || row.DropPath != drop || row.MaxMB != 1024 {
		t.Fatalf("row = %+v, want it tied to the login and the drop folder", row)
	}
}

func TestEnable_IsolatedLoginGetsTheSpaceAsItsQuota(t *testing.T) {
	w := newWorld()
	w.agent.isolation = true
	if _, _, err := w.enable(t, 700); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	create := w.agent.find("ftpaccount.create")
	if create["isolated"] != true || create["quota_mb"] != float64(700) {
		t.Fatalf("ftpaccount.create = %v, want isolated with quota_mb 700", create)
	}
}

func TestEnable_RefusesBeforeTouchingTheHost(t *testing.T) {
	cases := []struct {
		name  string
		setup func(w *world)
		maxMB uint32
		want  error
	}{
		{"not a files resource", func(w *world) { w.res["r1"].Kind = "calendar" }, 10, ErrNotFiles},
		{"already on", func(w *world) { w.drops.rows["r1"] = models.ScanDrop{ResourceID: "r1"} }, 10, ErrAlreadyOn},
		{"no package", func(w *world) { w.owner.PackageID = nil }, 10, ErrNoPackage},
		{"no FTP in the package", func(w *world) { w.pkg.MaxFTPAccounts = 0 }, 10, ErrNotInPackage},
		{"no space", func(*world) {}, 0, ErrSpace},
		{"more space than the package", func(*world) {}, 5001, ErrSpace},
		{"no Linux user", func(w *world) { w.owner.Username = nil }, 10, ErrNoAccount},
		{"an address that cannot name a folder", func(w *world) { w.res["r1"].LocalPart = strp(".x") }, 10, ErrName},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld()
			c.setup(w)
			if _, _, err := w.enable(t, c.maxMB); !errors.Is(err, c.want) {
				t.Fatalf("Enable = %v, want %v", err, c.want)
			}
			if len(w.agent.calls) != 0 {
				t.Fatalf("agent calls %v before refusing", w.agent.commands())
			}
		})
	}
}

func TestEnable_TheLoginCountsAgainstThePackage(t *testing.T) {
	w := newWorld()
	w.pkg.MaxFTPAccounts = 1
	w.accounts.rows["other"] = models.FtpAccount{ID: "other", UserID: "u1", Username: "alice_web"}
	if _, _, err := w.enable(t, 10); !errors.Is(err, repository.ErrFtpCapExceeded) {
		t.Fatalf("Enable = %v, want the FTP account cap", err)
	}
	if len(w.drops.rows) != 0 {
		t.Fatal("a row was saved without a login")
	}
}

func TestEnable_AFailedSaveRemovesTheLogin(t *testing.T) {
	w := newWorld()
	w.drops.createErr = errors.New("db down")
	if _, _, err := w.enable(t, 10); err == nil {
		t.Fatal("Enable succeeded with a failed save")
	}
	if len(w.accounts.rows) != 0 {
		t.Fatalf("logins left = %v, want the new login removed", w.accounts.rows)
	}
	if w.agent.find("ftpaccount.delete") == nil {
		t.Fatalf("agent calls = %v, want the login deleted on the host", w.agent.commands())
	}
}

func TestDisable_RemovesTheLoginThenTheRow(t *testing.T) {
	w := newWorld()
	if _, _, err := w.enable(t, 10); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if err := Disable(context.Background(), w.d, "r1"); err != nil {
		t.Fatalf("Disable: %v", err)
	}
	if len(w.accounts.rows) != 0 || len(w.drops.rows) != 0 {
		t.Fatalf("left logins %v and rows %v, want none", w.accounts.rows, w.drops.rows)
	}
	if err := Disable(context.Background(), w.d, "r1"); !errors.Is(err, ErrNotOn) {
		t.Fatalf("second Disable = %v, want ErrNotOn", err)
	}
}

func TestPass_MovesScansAndRecordsThem(t *testing.T) {
	w := newWorld()
	if _, _, err := w.enable(t, 10); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	w.agent.calls = nil
	w.agent.ingest = agentwire.ScanDropIngestResult{Moved: 3}

	Pass(context.Background(), w.d)
	p := w.agent.find("scandrop.ingest")
	if p["tenant_username"] != "alice" || p["scanner_username"] != "alice_scanner" ||
		p["folder_id"] != "b" || p["host_email"] != "scans@example.com" ||
		p["max_folder_bytes"] != float64(10<<20) || p["max_files"] != float64(FilesPerPass) {
		t.Fatalf("scandrop.ingest params = %v", p)
	}
	if got := w.drops.rows["r1"].IngestedTotal; got != 3 {
		t.Fatalf("ingested_total = %d, want 3", got)
	}

	// Nothing moved and the same (empty) problem: nothing is written.
	w.agent.ingest = agentwire.ScanDropIngestResult{}
	before := len(w.drops.recorded)
	Pass(context.Background(), w.d)
	if len(w.drops.recorded) != before {
		t.Fatal("an idle pass wrote the row")
	}
}

func TestPass_WaitsForTheSharedFolder(t *testing.T) {
	w := newWorld()
	if _, _, err := w.enable(t, 10); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	w.res["r1"].CollectionID = ""
	w.agent.calls = nil
	Pass(context.Background(), w.d)
	if w.agent.find("scandrop.ingest") != nil {
		t.Fatal("ingested before the shared folder existed")
	}
	if got := w.drops.rows["r1"].LastError; got != waitingForFolder {
		t.Fatalf("last_error = %q, want %q", got, waitingForFolder)
	}
}

func TestPass_ResourceGoneRemovesTheLoginAndTheRow(t *testing.T) {
	w := newWorld()
	if _, _, err := w.enable(t, 10); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	delete(w.res, "r1")
	w.agent.calls = nil
	Pass(context.Background(), w.d)
	if w.agent.find("ftpaccount.delete") == nil {
		t.Fatalf("agent calls = %v, want the scanner login deleted", w.agent.commands())
	}
	if len(w.accounts.rows) != 0 || len(w.drops.rows) != 0 {
		t.Fatalf("left logins %v and rows %v, want none", w.accounts.rows, w.drops.rows)
	}
}

func TestPass_LoginGoneTurnsTheScannerOff(t *testing.T) {
	w := newWorld()
	_, acct, err := w.enable(t, 10)
	if err != nil {
		t.Fatalf("Enable: %v", err)
	}
	delete(w.accounts.rows, acct.ID)
	w.agent.calls = nil
	Pass(context.Background(), w.d)
	if len(w.drops.rows) != 0 {
		t.Fatalf("rows = %v, want the scanner off", w.drops.rows)
	}
	if len(w.agent.calls) != 0 {
		t.Fatalf("agent calls %v for a scanner with no login", w.agent.commands())
	}
}

func TestPass_AgentFailureIsShownToTheOwner(t *testing.T) {
	w := newWorld()
	if _, _, err := w.enable(t, 10); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	w.agent.errOn["scandrop.ingest"] = errors.New("the drop folder is missing")
	Pass(context.Background(), w.d)
	if got := w.drops.rows["r1"].LastError; !strings.Contains(got, "missing") {
		t.Fatalf("last_error = %q, want the agent's reason", got)
	}
}
