package dirprivops

import (
	"context"
	"errors"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/auth"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
)

// --- validators (the single canonical copies; ported from the two adapters) --

func TestValidatePath(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"/secret", "/secret", false},
		{"/a/b/c", "/a/b/c", false},
		{"/trim/", "/trim", false},
		{"/", "/", false},
		{"/x_1.2-3", "/x_1.2-3", false},
		{"", "", true},
		{"no-leading-slash", "", true},
		{"/has space", "", true},
		{"/has//double", "", true},
		{"/../etc/passwd", "", true},
		{"/dot/../boom", "", true},
		{"/special$char", "", true},
	}
	for _, tc := range cases {
		got, err := ValidatePath(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ValidatePath(%q) = %q, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ValidatePath(%q) error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ValidatePath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestValidatePath_LengthCap(t *testing.T) {
	long := "/"
	for i := 0; i < 300; i++ {
		long += "a"
	}
	if _, err := ValidatePath(long); err == nil {
		t.Errorf("expected error on len > 255")
	}
}

func TestValidateRealm(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"", "Restricted", false},
		{"Staging", "Staging", false},
		{` ok `, "ok", false},
		{`bad"quote`, "", true},
		{`bad\back`, "", true},
		{"highÿascii", "", true},
		{string(make([]byte, 256)), "", true},
	}
	for _, tc := range cases {
		got, err := ValidateRealm(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ValidateRealm(%q) = %q, want error", tc.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("ValidateRealm(%q) error: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("ValidateRealm(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestValidateUsername(t *testing.T) {
	for _, u := range []string{"alice", "bob.smith", "a_b-c", "1234"} {
		if err := ValidateUsername(u); err != nil {
			t.Errorf("ValidateUsername(%q) error: %v", u, err)
		}
	}
	for _, u := range []string{"", "has space", "$evil", "ünicode", string(make([]byte, 65))} {
		if err := ValidateUsername(u); err == nil {
			t.Errorf("ValidateUsername(%q) = nil, want error", u)
		}
	}
}

func TestValidatePassword(t *testing.T) {
	if err := ValidatePassword("short"); err == nil {
		t.Errorf("expected error on short")
	}
	if err := ValidatePassword("12345678"); err != nil {
		t.Errorf("8-char password rejected: %v", err)
	}
	long := make([]byte, 129)
	for i := range long {
		long[i] = 'a'
	}
	if err := ValidatePassword(string(long)); err == nil {
		t.Errorf("expected error on len > 128")
	}
}

func TestValidationErrorFields(t *testing.T) {
	var ve *ValidationError
	if _, err := ValidatePath("/a b"); !errors.As(err, &ve) || ve.Field != "path" {
		t.Errorf("path error field = %v", err)
	}
	if _, err := ValidateRealm(`x"y`); !errors.As(err, &ve) || ve.Field != "realm" {
		t.Errorf("realm error field = %v", err)
	}
	if err := ValidateUsername("bad user"); !errors.As(err, &ve) || ve.Field != "username" {
		t.Errorf("username error field = %v", err)
	}
	if err := ValidatePassword("x"); !errors.As(err, &ve) || ve.Field != "password" {
		t.Errorf("password error field = %v", err)
	}
}

// --- fake repo ---------------------------------------------------------------

type fakePrivacyRepo struct {
	repository.DomainDirectoryPrivacyRepository // embed; unused methods panic
	rules                                       map[string]*models.DomainDirectoryPrivacyRule
	creds                                       map[string]*models.DomainDirectoryPrivacyCredential

	createRuleErr error
	updateRuleErr error
	deleteRuleErr error
	createCredErr error
	deleteCredErr error

	createdRule   *models.DomainDirectoryPrivacyRule
	updatedRealm  string
	deletedRuleID string
	createdCred   *models.DomainDirectoryPrivacyCredential
	deletedCredID string
}

func newFakeRepo() *fakePrivacyRepo {
	return &fakePrivacyRepo{
		rules: map[string]*models.DomainDirectoryPrivacyRule{},
		creds: map[string]*models.DomainDirectoryPrivacyCredential{},
	}
}

func (f *fakePrivacyRepo) FindRuleByID(_ context.Context, id string) (*models.DomainDirectoryPrivacyRule, error) {
	if r, ok := f.rules[id]; ok {
		return r, nil
	}
	return nil, repository.ErrNotFound
}
func (f *fakePrivacyRepo) CreateRule(_ context.Context, row *models.DomainDirectoryPrivacyRule) error {
	if f.createRuleErr != nil {
		return f.createRuleErr
	}
	f.createdRule = row
	return nil
}
func (f *fakePrivacyRepo) UpdateRule(_ context.Context, id, realm string) error {
	if f.updateRuleErr != nil {
		return f.updateRuleErr
	}
	f.updatedRealm = realm
	return nil
}
func (f *fakePrivacyRepo) DeleteRule(_ context.Context, id string) error {
	if f.deleteRuleErr != nil {
		return f.deleteRuleErr
	}
	f.deletedRuleID = id
	return nil
}
func (f *fakePrivacyRepo) FindCredentialByID(_ context.Context, id string) (*models.DomainDirectoryPrivacyCredential, error) {
	if c, ok := f.creds[id]; ok {
		return c, nil
	}
	return nil, repository.ErrNotFound
}
func (f *fakePrivacyRepo) CreateCredential(_ context.Context, row *models.DomainDirectoryPrivacyCredential) error {
	if f.createCredErr != nil {
		return f.createCredErr
	}
	f.createdCred = row
	return nil
}
func (f *fakePrivacyRepo) DeleteCredential(_ context.Context, id string) error {
	if f.deleteCredErr != nil {
		return f.deleteCredErr
	}
	f.deletedCredID = id
	return nil
}

func depsFor(repo *fakePrivacyRepo, sched *int) Deps {
	return Deps{
		Privacy:    repo,
		BcryptCost: bcrypt.MinCost,
		Schedule:   func(string) { *sched++ },
	}
}

// --- ops contract ------------------------------------------------------------

func TestCreateRule_SchedulesOnceAndCleansPath(t *testing.T) {
	repo := newFakeRepo()
	sched := 0
	row, err := CreateRule(context.Background(), depsFor(repo, &sched), "D1", "/admin/", "Staff")
	if err != nil {
		t.Fatalf("CreateRule: %v", err)
	}
	if sched != 1 {
		t.Errorf("schedule calls = %d, want 1", sched)
	}
	if row.Path != "/admin" || row.Realm != "Staff" || row.DomainID != "D1" || row.ID == "" {
		t.Errorf("row = %+v", row)
	}
	if repo.createdRule == nil {
		t.Errorf("CreateRule not persisted")
	}
}

func TestCreateRule_RejectsBadPathNoSchedule(t *testing.T) {
	repo := newFakeRepo()
	sched := 0
	_, err := CreateRule(context.Background(), depsFor(repo, &sched), "D1", "/../etc", "")
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Field != "path" {
		t.Fatalf("want path ValidationError, got %v", err)
	}
	if sched != 0 || repo.createdRule != nil {
		t.Errorf("bad path must not persist/schedule (sched=%d, created=%v)", sched, repo.createdRule)
	}
}

func TestCreateRule_RepoErrorNoSchedule(t *testing.T) {
	repo := newFakeRepo()
	repo.createRuleErr = errors.New("db down")
	sched := 0
	if _, err := CreateRule(context.Background(), depsFor(repo, &sched), "D1", "/ok", ""); err == nil {
		t.Fatal("expected repo error")
	}
	if sched != 0 {
		t.Errorf("repo failure must not schedule, got %d", sched)
	}
}

func TestUpdateRule_ContainmentAndSchedule(t *testing.T) {
	repo := newFakeRepo()
	repo.rules["R1"] = &models.DomainDirectoryPrivacyRule{ID: "R1", DomainID: "D1", Path: "/a", Realm: "old"}
	sched := 0
	row, err := UpdateRule(context.Background(), depsFor(repo, &sched), "D1", "R1", "new")
	if err != nil {
		t.Fatalf("UpdateRule: %v", err)
	}
	if row.Realm != "new" || repo.updatedRealm != "new" || sched != 1 {
		t.Errorf("update wrong: realm=%q updated=%q sched=%d", row.Realm, repo.updatedRealm, sched)
	}

	// Rule belongs to a different domain → not found, no write.
	sched = 0
	repo.updatedRealm = ""
	if _, err := UpdateRule(context.Background(), depsFor(repo, &sched), "OTHER", "R1", "x"); !errors.Is(err, ErrRuleNotFound) {
		t.Fatalf("want ErrRuleNotFound, got %v", err)
	}
	if sched != 0 || repo.updatedRealm != "" {
		t.Errorf("cross-domain update must not write/schedule")
	}

	// Bad realm → validation error, no write.
	sched = 0
	if _, err := UpdateRule(context.Background(), depsFor(repo, &sched), "D1", "R1", "bad\"realm"); err == nil {
		t.Fatal("expected realm validation error")
	}
	if sched != 0 {
		t.Errorf("invalid realm must not schedule")
	}
}

func TestDeleteRule_ContainmentAndSchedule(t *testing.T) {
	repo := newFakeRepo()
	repo.rules["R1"] = &models.DomainDirectoryPrivacyRule{ID: "R1", DomainID: "D1"}
	sched := 0
	if err := DeleteRule(context.Background(), depsFor(repo, &sched), "D1", "R1"); err != nil {
		t.Fatalf("DeleteRule: %v", err)
	}
	if repo.deletedRuleID != "R1" || sched != 1 {
		t.Errorf("delete wrong: deleted=%q sched=%d", repo.deletedRuleID, sched)
	}

	sched = 0
	repo.deletedRuleID = ""
	if err := DeleteRule(context.Background(), depsFor(repo, &sched), "D1", "NOPE"); !errors.Is(err, ErrRuleNotFound) {
		t.Fatalf("want ErrRuleNotFound, got %v", err)
	}
	if sched != 0 || repo.deletedRuleID != "" {
		t.Errorf("missing rule must not delete/schedule")
	}
}

func TestCreateCredential_HashAndSchedule(t *testing.T) {
	repo := newFakeRepo()
	repo.rules["R1"] = &models.DomainDirectoryPrivacyRule{ID: "R1", DomainID: "D1"}
	sched := 0
	row, err := CreateCredential(context.Background(), depsFor(repo, &sched), "D1", "R1", "alice", "s3cretpw")
	if err != nil {
		t.Fatalf("CreateCredential: %v", err)
	}
	if sched != 1 || repo.createdCred == nil || row.Username != "alice" {
		t.Errorf("create wrong: sched=%d created=%v user=%q", sched, repo.createdCred, row.Username)
	}
	if row.PasswordHash == "s3cretpw" || !auth.VerifyPassword(row.PasswordHash, "s3cretpw") {
		t.Errorf("password not hashed at write")
	}
}

func TestCreateCredential_ValidationNoSchedule(t *testing.T) {
	repo := newFakeRepo()
	repo.rules["R1"] = &models.DomainDirectoryPrivacyRule{ID: "R1", DomainID: "D1"}

	sched := 0
	_, err := CreateCredential(context.Background(), depsFor(repo, &sched), "D1", "R1", "bad user", "s3cretpw")
	var ve *ValidationError
	if !errors.As(err, &ve) || ve.Field != "username" {
		t.Fatalf("want username ValidationError, got %v", err)
	}
	if sched != 0 || repo.createdCred != nil {
		t.Errorf("bad username must not persist/schedule")
	}

	sched = 0
	_, err = CreateCredential(context.Background(), depsFor(repo, &sched), "D1", "R1", "alice", "short")
	if !errors.As(err, &ve) || ve.Field != "password" {
		t.Fatalf("want password ValidationError, got %v", err)
	}
	if sched != 0 {
		t.Errorf("bad password must not schedule")
	}

	// Rule on another domain → not found before any validation write.
	sched = 0
	if _, err := CreateCredential(context.Background(), depsFor(repo, &sched), "OTHER", "R1", "alice", "s3cretpw"); !errors.Is(err, ErrRuleNotFound) {
		t.Fatalf("want ErrRuleNotFound, got %v", err)
	}
	if sched != 0 {
		t.Errorf("cross-domain credential must not schedule")
	}
}

func TestDeleteCredential_Containment(t *testing.T) {
	repo := newFakeRepo()
	repo.rules["R1"] = &models.DomainDirectoryPrivacyRule{ID: "R1", DomainID: "D1"}
	repo.rules["R2"] = &models.DomainDirectoryPrivacyRule{ID: "R2", DomainID: "D1"}
	repo.creds["C1"] = &models.DomainDirectoryPrivacyCredential{ID: "C1", RuleID: "R1"}
	repo.creds["C2"] = &models.DomainDirectoryPrivacyCredential{ID: "C2", RuleID: "R2"}

	// Happy path: C1 under R1.
	sched := 0
	if err := DeleteCredential(context.Background(), depsFor(repo, &sched), "D1", "R1", "C1"); err != nil {
		t.Fatalf("DeleteCredential: %v", err)
	}
	if repo.deletedCredID != "C1" || sched != 1 {
		t.Errorf("delete wrong: deleted=%q sched=%d", repo.deletedCredID, sched)
	}

	// Cross-rule: C2 belongs to R2, operator names R1 → fail closed.
	sched = 0
	repo.deletedCredID = ""
	if err := DeleteCredential(context.Background(), depsFor(repo, &sched), "D1", "R1", "C2"); !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("want ErrCredentialNotFound, got %v", err)
	}
	if sched != 0 || repo.deletedCredID != "" {
		t.Errorf("cross-rule delete must not delete/schedule (deleted=%q sched=%d)", repo.deletedCredID, sched)
	}

	// Missing credential → fail closed.
	sched = 0
	if err := DeleteCredential(context.Background(), depsFor(repo, &sched), "D1", "R1", "NOPE"); !errors.Is(err, ErrCredentialNotFound) {
		t.Fatalf("want ErrCredentialNotFound, got %v", err)
	}
	if sched != 0 {
		t.Errorf("missing credential must not schedule")
	}

	// Missing rule → rule not found.
	sched = 0
	if err := DeleteCredential(context.Background(), depsFor(repo, &sched), "D1", "NOPE", "C1"); !errors.Is(err, ErrRuleNotFound) {
		t.Fatalf("want ErrRuleNotFound, got %v", err)
	}
}

func TestSchedule_NilIsNoop(t *testing.T) {
	repo := newFakeRepo()
	// CLI wiring: Schedule nil, BcryptCost 0 (defaults). Must not panic.
	deps := Deps{Privacy: repo}
	if _, err := CreateRule(context.Background(), deps, "D1", "/ok", ""); err != nil {
		t.Fatalf("CreateRule with nil Schedule: %v", err)
	}
}
