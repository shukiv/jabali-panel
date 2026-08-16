package drsync

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

// fakeAgent maps a command name to a handler so each test wires only the
// commands it exercises. An unmapped command returns an error, which surfaces
// as a recorded status=error — exactly what a real missing/failed call does.
type fakeAgent struct {
	handlers map[string]func(params any) (json.RawMessage, error)
	calls    []string
}

func (a *fakeAgent) Call(_ context.Context, command string, params any) (json.RawMessage, error) {
	a.calls = append(a.calls, command)
	h, ok := a.handlers[command]
	if !ok {
		return nil, errors.New("unexpected agent command: " + command)
	}
	return h(params)
}

func manifestsJSON(ids ...string) func(any) (json.RawMessage, error) {
	return func(any) (json.RawMessage, error) {
		type row struct {
			SnapshotID string `json:"snapshot_id"`
		}
		rows := make([]row, 0, len(ids))
		for _, id := range ids {
			rows = append(rows, row{SnapshotID: id})
		}
		b, _ := json.Marshal(map[string]any{"manifests": rows, "total": len(rows)})
		return b, nil
	}
}

// fakeSettings is a one-row settings store capturing the last RecordDRSync args.
type fakeSettings struct {
	s      *models.ServerSettings
	getErr error

	recorded      bool
	recSnapshotID string
	recStatus     string
	recErr        string

	reassertCalls  int
	reassertDestID string
	reassertPeer   string
	reassertErr    error
}

func (f *fakeSettings) Get(context.Context) (*models.ServerSettings, error) {
	return f.s, f.getErr
}

func (f *fakeSettings) RecordDRSync(_ context.Context, snapshotID, status, syncErr string) error {
	f.recorded = true
	f.recSnapshotID, f.recStatus, f.recErr = snapshotID, status, syncErr
	return nil
}

func (f *fakeSettings) ReassertDRPairing(_ context.Context, destinationID, peerLabel string, _ *time.Time) error {
	f.reassertCalls++
	if f.reassertErr != nil {
		return f.reassertErr
	}
	f.reassertDestID, f.reassertPeer = destinationID, peerLabel
	return nil
}

type fakeDests struct {
	byID map[string]*models.BackupDestination
	err  error
}

func (d *fakeDests) Get(_ context.Context, id string) (*models.BackupDestination, error) {
	if d.err != nil {
		return nil, d.err
	}
	return d.byID[id], nil // nil => not found
}

func (d *fakeDests) GetByName(_ context.Context, name string) (*models.BackupDestination, error) {
	if d.err != nil {
		return nil, d.err
	}
	for _, dest := range d.byID {
		if dest != nil && dest.Name == name {
			return dest, nil
		}
	}
	return nil, errors.New("not found")
}

func standbySettings(destID, lastSnap string) *models.ServerSettings {
	id := destID
	return &models.ServerSettings{
		ServerRole:       models.ServerRoleStandby,
		DRDestinationID:  &id,
		DRLastSnapshotID: lastSnap,
	}
}

func enabledDest(id string) *models.BackupDestination {
	return &models.BackupDestination{ID: id, URL: "sftp:host:/repo", Kind: models.BackupDestinationKindLocal, Enabled: true}
}

func newSyncer(t *testing.T, s settingsStore, d destinationStore, a *fakeAgent) *Syncer {
	t.Helper()
	sy := New(Deps{Settings: s, Destinations: d, Agent: a})
	if sy == nil {
		t.Fatal("New returned nil with all required deps present")
	}
	return sy
}

func TestSyncOnce_PrimaryIsInert(t *testing.T) {
	fs := &fakeSettings{s: &models.ServerSettings{ServerRole: models.ServerRolePrimary}}
	fa := &fakeAgent{}
	res, err := newSyncer(t, fs, &fakeDests{}, fa).SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !res.Skipped {
		t.Errorf("primary must be skipped, got %+v", res)
	}
	if fs.recorded {
		t.Error("primary must not record any sync state")
	}
	if len(fa.calls) != 0 {
		t.Errorf("primary must make no agent calls, made %v", fa.calls)
	}
}

func TestSyncOnce_UnseededIsInert(t *testing.T) {
	fs := &fakeSettings{s: &models.ServerSettings{}} // role "" → primary
	res, err := newSyncer(t, fs, &fakeDests{}, &fakeAgent{}).SyncOnce(context.Background())
	if err != nil || !res.Skipped {
		t.Fatalf("unseeded row must be skipped: res=%+v err=%v", res, err)
	}
}

func TestSyncOnce_SettingsReadErrorPropagates(t *testing.T) {
	fs := &fakeSettings{getErr: errors.New("db down")}
	res, err := newSyncer(t, fs, &fakeDests{}, &fakeAgent{}).SyncOnce(context.Background())
	if err == nil {
		t.Fatal("settings read error must propagate to caller for logging")
	}
	if res.Skipped {
		t.Error("a read error is not a skip")
	}
}

func TestSyncOnce_StandbyWithoutDestinationRecordsError(t *testing.T) {
	fs := &fakeSettings{s: &models.ServerSettings{ServerRole: models.ServerRoleStandby}} // DRDestinationID nil
	res, _ := newSyncer(t, fs, &fakeDests{}, &fakeAgent{}).SyncOnce(context.Background())
	if res.Status != models.DRSyncStatusError {
		t.Errorf("want error status, got %q", res.Status)
	}
	if !fs.recorded || fs.recStatus != models.DRSyncStatusError {
		t.Errorf("error must be recorded, got recorded=%v status=%q", fs.recorded, fs.recStatus)
	}
}

func TestSyncOnce_DestinationNotFoundRecordsError(t *testing.T) {
	fs := &fakeSettings{s: standbySettings("D1", "")}
	res, _ := newSyncer(t, fs, &fakeDests{byID: map[string]*models.BackupDestination{}}, &fakeAgent{}).SyncOnce(context.Background())
	if res.Status != models.DRSyncStatusError || fs.recStatus != models.DRSyncStatusError {
		t.Errorf("missing destination must record error, got %+v", res)
	}
}

func TestSyncOnce_DisabledDestinationRecordsError(t *testing.T) {
	d := enabledDest("D1")
	d.Enabled = false
	fs := &fakeSettings{s: standbySettings("D1", "")}
	res, _ := newSyncer(t, fs, &fakeDests{byID: map[string]*models.BackupDestination{"D1": d}}, &fakeAgent{}).SyncOnce(context.Background())
	if res.Status != models.DRSyncStatusError {
		t.Errorf("disabled destination must record error, got %q", res.Status)
	}
}

func TestSyncOnce_NoManifestYetIsWaiting(t *testing.T) {
	fs := &fakeSettings{s: standbySettings("D1", "")}
	fa := &fakeAgent{handlers: map[string]func(any) (json.RawMessage, error){
		"system.restore_list_manifests": manifestsJSON(), // empty
	}}
	res, _ := newSyncer(t, fs, &fakeDests{byID: map[string]*models.BackupDestination{"D1": enabledDest("D1")}}, fa).SyncOnce(context.Background())
	if res.Status != models.DRSyncStatusWaiting {
		t.Errorf("empty repo must be waiting, got %q", res.Status)
	}
	for _, c := range fa.calls {
		if c == "system.restore" {
			t.Error("must NOT restore when there is no manifest")
		}
	}
	if fs.recStatus != models.DRSyncStatusWaiting {
		t.Errorf("waiting must be recorded, got %q", fs.recStatus)
	}
}

func TestSyncOnce_AlreadyCurrentSkipsRestore(t *testing.T) {
	fs := &fakeSettings{s: standbySettings("D1", "SNAP_A")}
	fa := &fakeAgent{handlers: map[string]func(any) (json.RawMessage, error){
		"system.restore_list_manifests": manifestsJSON("SNAP_A"),
	}}
	res, _ := newSyncer(t, fs, &fakeDests{byID: map[string]*models.BackupDestination{"D1": enabledDest("D1")}}, fa).SyncOnce(context.Background())
	if res.Status != models.DRSyncStatusCurrent {
		t.Errorf("want current, got %q", res.Status)
	}
	for _, c := range fa.calls {
		if c == "system.restore" {
			t.Error("must NOT restore when already at newest snapshot")
		}
	}
}

func TestSyncOnce_NewerSnapshotRestoresAndRecords(t *testing.T) {
	fs := &fakeSettings{s: standbySettings("D1", "SNAP_OLD")}
	var restoreParams map[string]any
	fa := &fakeAgent{handlers: map[string]func(any) (json.RawMessage, error){
		"system.restore_list_manifests": manifestsJSON("SNAP_NEW", "SNAP_OLD"),
		"system.restore": func(p any) (json.RawMessage, error) {
			restoreParams, _ = p.(map[string]any)
			return json.RawMessage(`{"job_id":"x"}`), nil
		},
	}}
	res, _ := newSyncer(t, fs, &fakeDests{byID: map[string]*models.BackupDestination{"D1": enabledDest("D1")}}, fa).SyncOnce(context.Background())
	if res.Status != models.DRSyncStatusOK || res.SnapshotID != "SNAP_NEW" {
		t.Fatalf("want ok/SNAP_NEW, got %+v", res)
	}
	if restoreParams["manifest_snapshot_id"] != "SNAP_NEW" {
		t.Errorf("restore must target newest snapshot, got %v", restoreParams["manifest_snapshot_id"])
	}
	if restoreParams["apply"] != true {
		t.Errorf("restore must apply:true, got %v", restoreParams["apply"])
	}
	if restoreParams["include_accounts"] != false {
		t.Errorf("standby restore must NOT include accounts, got %v", restoreParams["include_accounts"])
	}
	if fs.recStatus != models.DRSyncStatusOK || fs.recSnapshotID != "SNAP_NEW" {
		t.Errorf("ok must record the applied snapshot, got status=%q snap=%q", fs.recStatus, fs.recSnapshotID)
	}
}

func TestSyncOnce_ListErrorRecordsErrorNotSnapshot(t *testing.T) {
	fs := &fakeSettings{s: standbySettings("D1", "SNAP_OLD")}
	fa := &fakeAgent{handlers: map[string]func(any) (json.RawMessage, error){
		"system.restore_list_manifests": func(any) (json.RawMessage, error) {
			return nil, errors.New("destination unreachable")
		},
	}}
	res, _ := newSyncer(t, fs, &fakeDests{byID: map[string]*models.BackupDestination{"D1": enabledDest("D1")}}, fa).SyncOnce(context.Background())
	if res.Status != models.DRSyncStatusError {
		t.Errorf("list failure must be error, got %q", res.Status)
	}
	if fs.recSnapshotID != "" {
		t.Errorf("a failed tick must not stamp an applied snapshot, got %q", fs.recSnapshotID)
	}
}

func TestSyncOnce_RestoreErrorDoesNotAdvanceSnapshot(t *testing.T) {
	fs := &fakeSettings{s: standbySettings("D1", "SNAP_OLD")}
	fa := &fakeAgent{handlers: map[string]func(any) (json.RawMessage, error){
		"system.restore_list_manifests": manifestsJSON("SNAP_NEW"),
		"system.restore": func(any) (json.RawMessage, error) {
			return nil, errors.New("restic restore failed")
		},
	}}
	res, _ := newSyncer(t, fs, &fakeDests{byID: map[string]*models.BackupDestination{"D1": enabledDest("D1")}}, fa).SyncOnce(context.Background())
	if res.Status != models.DRSyncStatusError {
		t.Errorf("restore failure must be error, got %q", res.Status)
	}
	if fs.recSnapshotID != "" {
		t.Errorf("failed restore must not advance last-applied snapshot, got %q", fs.recSnapshotID)
	}
}

func TestNew_NilWhenRequiredDepMissing(t *testing.T) {
	if New(Deps{Destinations: &fakeDests{}, Agent: &fakeAgent{}}) != nil {
		t.Error("nil Settings must yield nil Syncer")
	}
	if New(Deps{Settings: &fakeSettings{}, Agent: &fakeAgent{}}) != nil {
		t.Error("nil Destinations must yield nil Syncer")
	}
	if New(Deps{Settings: &fakeSettings{}, Destinations: &fakeDests{}}) != nil {
		t.Error("nil Agent must yield nil Syncer")
	}
}

// namedDest builds an enabled destination with an explicit name — the DR
// channel identity the reassert step remaps by.
func namedDest(id, name string) *models.BackupDestination {
	d := enabledDest(id)
	d.Name = name
	return d
}

// GH #331 two-node drill finding: an applied restore loads the PRIMARY's
// panel DB over this box — server_settings (role=primary, no pairing) and
// backup_destinations (the primary's rows, different ULIDs) included. The
// tick must re-assert the standby pairing afterwards, remapping the DR
// destination by name, or the standby self-demotes after its first
// successful sync.
func TestSyncOnce_AppliedRestoreReassertsPairing(t *testing.T) {
	pairedAt := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	fs := &fakeSettings{s: standbySettings("OLDDEST", "")}
	fs.s.DRPeerLabel = "182.54.236.60"
	fs.s.DRPairedAt = &pairedAt
	fd := &fakeDests{byID: map[string]*models.BackupDestination{"OLDDEST": namedDest("OLDDEST", "dr-channel")}}
	fa := &fakeAgent{handlers: map[string]func(any) (json.RawMessage, error){
		"system.restore_list_manifests": manifestsJSON("SNAP_NEW"),
		"system.restore": func(any) (json.RawMessage, error) {
			// Simulate the overwrite the real restore performs.
			fs.s = &models.ServerSettings{ServerRole: models.ServerRolePrimary}
			fd.byID = map[string]*models.BackupDestination{"NEWDEST": namedDest("NEWDEST", "dr-channel")}
			return json.RawMessage(`{}`), nil
		},
	}}
	res, err := newSyncer(t, fs, fd, fa).SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if res.Status != models.DRSyncStatusOK {
		t.Fatalf("status = %q, want ok (detail: %s)", res.Status, res.Detail)
	}
	if fs.reassertCalls == 0 {
		t.Fatal("applied restore must re-assert the standby pairing")
	}
	if fs.reassertDestID != "NEWDEST" {
		t.Errorf("pairing must remap the DR destination by name to the primary's row, got %q", fs.reassertDestID)
	}
	if fs.reassertPeer != "182.54.236.60" {
		t.Errorf("peer label must survive, got %q", fs.reassertPeer)
	}
}

// A tick that applied nothing (already current) must not touch the pairing.
func TestSyncOnce_CurrentTickDoesNotReassert(t *testing.T) {
	fs := &fakeSettings{s: standbySettings("D1", "SNAP_A")}
	fa := &fakeAgent{handlers: map[string]func(any) (json.RawMessage, error){
		"system.restore_list_manifests": manifestsJSON("SNAP_A"),
	}}
	if _, err := newSyncer(t, fs, &fakeDests{byID: map[string]*models.BackupDestination{"D1": enabledDest("D1")}}, fa).SyncOnce(context.Background()); err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if fs.reassertCalls != 0 {
		t.Errorf("current tick must not re-assert pairing, got %d calls", fs.reassertCalls)
	}
}

// A reassert failure after an applied restore is the dangerous half-state
// (box restored but reads as a primary) — it must surface as a recorded
// error, not silence.
func TestSyncOnce_ReassertFailureRecordsError(t *testing.T) {
	fs := &fakeSettings{s: standbySettings("D1", ""), reassertErr: errors.New("db gone")}
	fa := &fakeAgent{handlers: map[string]func(any) (json.RawMessage, error){
		"system.restore_list_manifests": manifestsJSON("SNAP_NEW"),
		"system.restore": func(any) (json.RawMessage, error) {
			return json.RawMessage(`{}`), nil
		},
	}}
	sy := newSyncer(t, fs, &fakeDests{byID: map[string]*models.BackupDestination{"D1": enabledDest("D1")}}, fa)
	sy.reassertRetryDelay = time.Millisecond
	res, err := sy.SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if res.Status != models.DRSyncStatusError {
		t.Fatalf("status = %q, want error", res.Status)
	}
	if fs.reassertCalls != 3 {
		t.Errorf("reassert must retry (3 attempts), got %d", fs.reassertCalls)
	}
	if fs.recStatus != models.DRSyncStatusError || fs.recErr == "" {
		t.Errorf("reassert failure must be recorded: status=%q err=%q", fs.recStatus, fs.recErr)
	}
}

// GH #331 drill finding: a paired-but-never-fed destination (restic exit 10,
// "repository does not exist") is the WAITING state, not an error — the
// operator just hasn't run `jabali dr feed` (or its first backup) yet.
func TestSyncOnce_UninitializedRepoIsWaiting(t *testing.T) {
	fs := &fakeSettings{s: standbySettings("D1", "")}
	fa := &fakeAgent{handlers: map[string]func(any) (json.RawMessage, error){
		"system.restore_list_manifests": func(any) (json.RawMessage, error) {
			return nil, errors.New(`restic snapshots: exit status 10 (stderr: Fatal: repository does not exist: unable to open config file)`)
		},
	}}
	res, err := newSyncer(t, fs, &fakeDests{byID: map[string]*models.BackupDestination{"D1": enabledDest("D1")}}, fa).SyncOnce(context.Background())
	if err != nil {
		t.Fatalf("SyncOnce: %v", err)
	}
	if res.Status != models.DRSyncStatusWaiting {
		t.Errorf("uninitialized repo must be waiting, got %q (detail %s)", res.Status, res.Detail)
	}
	if fs.recStatus != models.DRSyncStatusWaiting {
		t.Errorf("waiting must be recorded, got %q", fs.recStatus)
	}
}
