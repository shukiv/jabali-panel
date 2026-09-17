package dbconsoleops

import (
	"context"
	"errors"
	"testing"
)

func TestNormalizeEngine(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		// Empty and whitespace → mariadb default
		{"", "mariadb"},
		{" ", "mariadb"},
		{"\t", "mariadb"},
		{"\n", "mariadb"},

		// Valid engines preserved (trimmed)
		{"mariadb", "mariadb"},
		{"postgres", "postgres"},
		{" mariadb ", "mariadb"},
		{" postgres ", "postgres"},

		// Unknown engines passed through (validation is EnsureShadowForEngine's job)
		{"unknown", "unknown"},
		{"mysql", "mysql"},
		{"Oracle", "Oracle"},
	}

	for _, tt := range tests {
		t.Run("NormalizeEngine("+tt.input+")", func(t *testing.T) {
			got := NormalizeEngine(tt.input)
			if got != tt.expected {
				t.Errorf("got %q, want %q", got, tt.expected)
			}
		})
	}
}

// mockShadowService stubs the ShadowService interface for testing.
type mockShadowService struct {
	ensureShadowErr error
}

func (m *mockShadowService) EnsureShadow(ctx context.Context, userID string) error {
	return m.ensureShadowErr
}

// mockAdminerShadowService stubs the AdminerShadowService interface.
type mockAdminerShadowService struct {
	ensureShadowErr error
	ensurePgErr     error
}

func (m *mockAdminerShadowService) EnsureShadow(ctx context.Context, userID string) error {
	return m.ensureShadowErr
}

func (m *mockAdminerShadowService) EnsurePgShadow(ctx context.Context, userID string) error {
	return m.ensurePgErr
}

func TestEnsureShadowForEngine(t *testing.T) {
	ctx := context.Background()
	userID := "test_user_123"

	tests := []struct {
		name     string
		engine   string
		base     *mockShadowService
		adminer  *mockAdminerShadowService
		wantErr  error
		wantNil  bool
	}{
		// MariaDB path succeeds
		{
			name:     "mariadb success",
			engine:   "mariadb",
			base:     &mockShadowService{ensureShadowErr: nil},
			adminer:  &mockAdminerShadowService{},
			wantErr:  nil,
			wantNil:  true,
		},

		// MariaDB path fails
		{
			name:    "mariadb failure",
			engine:  "mariadb",
			base:    &mockShadowService{ensureShadowErr: errors.New("agent failed")},
			adminer: &mockAdminerShadowService{},
			wantErr: ErrShadowProvisioning,
			wantNil: false,
		},

		// PostgreSQL path succeeds
		{
			name:     "postgres success",
			engine:   "postgres",
			base:     &mockShadowService{},
			adminer:  &mockAdminerShadowService{ensurePgErr: nil},
			wantErr:  nil,
			wantNil:  true,
		},

		// PostgreSQL path fails
		{
			name:    "postgres failure",
			engine:  "postgres",
			base:    &mockShadowService{},
			adminer: &mockAdminerShadowService{ensurePgErr: errors.New("role creation failed")},
			wantErr: ErrShadowProvisioning,
			wantNil: false,
		},

		// Invalid engine
		{
			name:     "invalid engine",
			engine:   "oracle",
			base:     &mockShadowService{},
			adminer:  &mockAdminerShadowService{},
			wantErr:  ErrInvalidEngine,
			wantNil:  false,
		},

		// Unknown engine (after normalization)
		{
			name:     "unknown engine",
			engine:   "mysql",
			base:     &mockShadowService{},
			adminer:  &mockAdminerShadowService{},
			wantErr:  ErrInvalidEngine,
			wantNil:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := EnsureShadowForEngine(ctx, tt.engine, userID, tt.base, tt.adminer)

			if tt.wantNil {
				if err != nil {
					t.Errorf("want nil error, got %v", err)
				}
				return
			}

			if err == nil {
				t.Error("want error, got nil")
				return
			}

			if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
				t.Errorf("got error %v, want error containing %v", err, tt.wantErr)
			}
		})
	}
}

// Falsification tests: verify guards are present and working.

func TestEnsureShadowForEngine_Falsify_MariaDBGuard(t *testing.T) {
	// Guard: mariadb path must call base.EnsureShadow, not adminer.EnsurePgShadow
	ctx := context.Background()
	base := &mockShadowService{ensureShadowErr: errors.New("base was called")}
	adminer := &mockAdminerShadowService{ensurePgErr: errors.New("adminer was called")}

	err := EnsureShadowForEngine(ctx, "mariadb", "user123", base, adminer)

	// Error must come from base.EnsureShadow, not adminer.EnsurePgShadow
	if err == nil || !errors.Is(err, ErrShadowProvisioning) {
		t.Errorf("mariadb path did not call base.EnsureShadow: got %v", err)
	}
	if errors.Is(err, errors.New("adminer was called")) {
		t.Error("mariadb path incorrectly called adminer.EnsurePgShadow")
	}
}

func TestEnsureShadowForEngine_Falsify_PostgresGuard(t *testing.T) {
	// Guard: postgres path must call adminer.EnsurePgShadow, not base.EnsureShadow
	ctx := context.Background()
	base := &mockShadowService{ensureShadowErr: errors.New("base was called")}
	adminer := &mockAdminerShadowService{ensurePgErr: errors.New("adminer was called")}

	err := EnsureShadowForEngine(ctx, "postgres", "user123", base, adminer)

	// Error must come from adminer.EnsurePgShadow, not base.EnsureShadow
	if err == nil || !errors.Is(err, ErrShadowProvisioning) {
		t.Errorf("postgres path did not call adminer.EnsurePgShadow: got %v", err)
	}
	if errors.Is(err, errors.New("base was called")) {
		t.Error("postgres path incorrectly called base.EnsureShadow")
	}
}

func TestEnsureShadowForEngine_Falsify_EngineValidation(t *testing.T) {
	// Guard: invalid engine must reject BEFORE calling any shadow service
	ctx := context.Background()
	base := &mockShadowService{ensureShadowErr: errors.New("should not be called")}
	adminer := &mockAdminerShadowService{ensurePgErr: errors.New("should not be called")}

	err := EnsureShadowForEngine(ctx, "invalid", "user123", base, adminer)

	if !errors.Is(err, ErrInvalidEngine) {
		t.Errorf("invalid engine: got %v, want ErrInvalidEngine", err)
	}
	if errors.Is(err, errors.New("should not be called")) {
		t.Error("invalid engine did not short-circuit: shadow service was called")
	}
}
