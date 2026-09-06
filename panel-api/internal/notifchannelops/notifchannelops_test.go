package notifchannelops

import (
	"errors"
	"testing"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/models"
)

func gateOn(kinds ...string) *models.ServerSettings {
	return &models.ServerSettings{
		TenantNotificationsEnabled: true,
		TenantNotificationKinds:    models.TenantNotificationKinds(kinds),
	}
}

func TestCheckKindAllowed(t *testing.T) {
	cases := []struct {
		name string
		st   *models.ServerSettings
		kind string
		want error
	}{
		{"nil settings fail closed", nil, models.NotificationChannelKindNtfy, ErrTenantNotificationsDisabled},
		{"gate off fail closed", &models.ServerSettings{TenantNotificationsEnabled: false}, models.NotificationChannelKindNtfy, ErrTenantNotificationsDisabled},
		{"default allowlist allows ntfy", gateOn(), models.NotificationChannelKindNtfy, nil},
		{"default allowlist denies email", gateOn(), models.NotificationChannelKindEmail, ErrKindNotAllowed},
		{"default allowlist denies webhook", gateOn(), models.NotificationChannelKindWebhook, ErrKindNotAllowed},
		{"explicit allowlist allows email", gateOn(models.NotificationChannelKindEmail), models.NotificationChannelKindEmail, nil},
		{"unknown kind denied", gateOn(), "carrier-pigeon", ErrKindNotAllowed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckKindAllowed(tc.st, tc.kind)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("want nil, got %v", err)
				}
				return
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("want errors.Is(%v), got %v", tc.want, err)
			}
		})
	}
}

func TestCheckQuota(t *testing.T) {
	if err := CheckQuota(MaxTenantChannelsPerUser - 1); err != nil {
		t.Fatalf("below limit should pass, got %v", err)
	}
	if err := CheckQuota(MaxTenantChannelsPerUser); !errors.Is(err, ErrTooManyChannels) {
		t.Fatalf("at limit should be ErrTooManyChannels, got %v", err)
	}
	if err := CheckQuota(MaxTenantChannelsPerUser + 1); !errors.Is(err, ErrTooManyChannels) {
		t.Fatalf("over limit should be ErrTooManyChannels, got %v", err)
	}
}

func TestForceOwnEmailConfig_NoOwnerEmail(t *testing.T) {
	cfg := models.NotificationChannelConfig{ToEmail: "attacker@evil.example"}
	if err := ForceOwnEmailConfig("", &cfg); !errors.Is(err, ErrNoAccountEmail) {
		t.Fatalf("empty owner email should be ErrNoAccountEmail, got %v", err)
	}
}

func TestForceOwnEmailConfig_RewritesEveryDestinationField(t *testing.T) {
	// A config that tries to smuggle an arbitrary destination + custom SMTP relay
	// (the open-relay / SSRF attack the forcing exists to stop).
	cfg := models.NotificationChannelConfig{
		Priority:     5, // non-destination display field, must survive
		ToEmail:      "victim@elsewhere.example",
		FromEmail:    "spoofed@elsewhere.example",
		SMTPMode:     "smtp",
		SMTPHost:     "169.254.169.254",
		SMTPPort:     2525,
		SMTPTLS:      "none",
		SMTPUsername: "attacker",
		SMTPPassword: "hunter2",
	}
	if err := ForceOwnEmailConfig("owner@jabali.example", &cfg); err != nil {
		t.Fatalf("valid owner email should force, got %v", err)
	}
	// Destination + transport hard-set to the safe own-account/local values.
	if cfg.ToEmail != "owner@jabali.example" {
		t.Errorf("ToEmail = %q, want owner", cfg.ToEmail)
	}
	if cfg.FromEmail != "owner@jabali.example" {
		t.Errorf("FromEmail = %q, want owner", cfg.FromEmail)
	}
	if cfg.SMTPMode != "local" {
		t.Errorf("SMTPMode = %q, want local", cfg.SMTPMode)
	}
	// Every caller-supplied relay field must be cleared — assert each one.
	if cfg.SMTPHost != "" {
		t.Errorf("SMTPHost = %q, want empty", cfg.SMTPHost)
	}
	if cfg.SMTPPort != 0 {
		t.Errorf("SMTPPort = %d, want 0", cfg.SMTPPort)
	}
	if cfg.SMTPTLS != "" {
		t.Errorf("SMTPTLS = %q, want empty", cfg.SMTPTLS)
	}
	if cfg.SMTPUsername != "" {
		t.Errorf("SMTPUsername = %q, want empty", cfg.SMTPUsername)
	}
	if cfg.SMTPPassword != "" {
		t.Errorf("SMTPPassword = %q, want empty", cfg.SMTPPassword)
	}
	// A non-destination field is preserved.
	if cfg.Priority != 5 {
		t.Errorf("Priority = %d, want 5 preserved", cfg.Priority)
	}
}
