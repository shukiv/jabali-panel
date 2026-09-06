package packageops

import (
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/limits"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/phppoolops"
)

func strptr(s string) *string { return &s }

// TestValidate exercises every invariant Validate guards. The limit-bound rows
// mirror internal/limits/resolve_test.go so the two stay in lockstep; the FPM /
// retention / kinds / nspawn rows prove those checks are actually reachable
// (not dead branches) — the point the reviewer flagged, since the CLI can't set
// those fields yet.
func TestValidate(t *testing.T) {
	valid := func() *models.HostingPackage {
		// A row that passes every check: zero limits (unlimited), and the
		// FPM/backup fields at the values a fresh INSERT gets from the column
		// defaults.
		return &models.HostingPackage{
			Name:                  "ok",
			FpmMaxChildrenCap:     20,
			FpmWorkerMemMb:        64,
			FpmVersionDefaults:    "{}",
			BackupRetentionPolicy: models.BackupRetentionReject,
		}
	}

	tests := []struct {
		name    string
		mutate  func(p *models.HostingPackage)
		wantErr bool
		is      error // when set, the returned error must errors.Is this
	}{
		{"all zero limits pass (unlimited is legal)", func(p *models.HostingPackage) {}, false, nil},
		{"sane limits pass", func(p *models.HostingPackage) {
			p.DiskQuotaMB, p.CPUQuotaPercent, p.MemoryLimitMB, p.MaxTasks = 5120, 200, 4096, 500
		}, false, nil},

		// Resource-limit bounds — mirror limits/resolve_test.go:128-132.
		{"cpu at max passes", func(p *models.HostingPackage) { p.CPUQuotaPercent = limits.MaxCPUQuotaPercent }, false, nil},
		{"cpu over max fails", func(p *models.HostingPackage) { p.CPUQuotaPercent = limits.MaxCPUQuotaPercent + 1 }, true, nil},
		{"memory over max fails", func(p *models.HostingPackage) { p.MemoryLimitMB = limits.MaxMemoryLimitMB + 1 }, true, nil},
		{"io_read over max fails", func(p *models.HostingPackage) { p.IOReadMbps = limits.MaxIOMbps + 1 }, true, nil},
		{"io_write over max fails", func(p *models.HostingPackage) { p.IOWriteMbps = limits.MaxIOMbps + 1 }, true, nil},
		{"tasks over max fails", func(p *models.HostingPackage) { p.MaxTasks = limits.MaxTasks + 1 }, true, nil},
		// Disk is intentionally unbounded — pins the "not our job" boundary.
		{"huge disk passes (unbounded)", func(p *models.HostingPackage) { p.DiskQuotaMB = 1 << 31 }, false, nil},

		// FPM children cap — reachable only through this check today.
		{"fpm cap at admin max passes", func(p *models.HostingPackage) { p.FpmMaxChildrenCap = phppoolops.AdminMaxChildrenCap }, false, nil},
		{"fpm cap over admin max fails", func(p *models.HostingPackage) { p.FpmMaxChildrenCap = phppoolops.AdminMaxChildrenCap + 1 }, true, ErrFpmCapTooHigh},

		// Backup retention policy.
		{"empty retention passes (treated as reject)", func(p *models.HostingPackage) { p.BackupRetentionPolicy = "" }, false, nil},
		{"prune retention passes", func(p *models.HostingPackage) { p.BackupRetentionPolicy = models.BackupRetentionPrune }, false, nil},
		{"bogus retention fails", func(p *models.HostingPackage) { p.BackupRetentionPolicy = "delete-everything" }, true, ErrInvalidBackupRetentionPolicy},

		// Backup destination kinds CSV.
		{"unknown backup kind fails", func(p *models.HostingPackage) { p.AllowedBackupDestinationKinds = "s3,not-a-kind" }, true, nil},

		// Nspawn image name.
		{"nil nspawn image passes", func(p *models.HostingPackage) { p.NspawnImageVersion = nil }, false, nil},
		{"valid nspawn image passes", func(p *models.HostingPackage) { p.NspawnImageVersion = strptr("bookworm-2026-01") }, false, nil},
		{"bad nspawn image fails", func(p *models.HostingPackage) { p.NspawnImageVersion = strptr("Bad/Image!") }, true, ErrInvalidNspawnImage},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := valid()
			tc.mutate(p)
			err := Validate(p)
			if tc.wantErr && err == nil {
				t.Fatalf("Validate() = nil, want error")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}
			if tc.is != nil && !errors.Is(err, tc.is) {
				t.Fatalf("Validate() error = %v, want errors.Is %v", err, tc.is)
			}
		})
	}
}
