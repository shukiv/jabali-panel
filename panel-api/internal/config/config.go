// Package config loads, validates, and exposes panel runtime configuration.
//
// Precedence, highest wins:
//  1. environment variables (PANEL_*, LOG_*, DATABASE_URL, JWT_*, AGENT_*,
//     CORS_ALLOWED_ORIGINS).
//  2. TOML file passed to Load (usually /etc/jabali/config.toml).
//  3. Built-in defaults (Defaults()).
//
// A missing config file is not an error — defaults + env are enough to boot.
// Invalid TOML, invalid values, and missing-in-production secrets all fail
// fast with a descriptive error.
package config

import (
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

// Exported environment names, referenced by callers that want to branch on
// server.env (e.g. tighter logging or stricter error messages in prod).
const (
	EnvDevelopment = "development"
	EnvProduction  = "production"
)

// Config is the root configuration struct. Sections map to TOML tables and to
// grouped env-var prefixes.
type Config struct {
	Server    ServerConfig    `toml:"server"`
	Log       LogConfig       `toml:"log"`
	Database  DatabaseConfig  `toml:"database"`
	Auth      AuthConfig      `toml:"auth"`
	Agent     AgentConfig     `toml:"agent"`
	CORS      CORSConfig      `toml:"cors"`
	PDNS      PDNSConfig      `toml:"pdns"`
	ACME      ACMEConfig      `toml:"acme"`
	SSO       SSOConfig       `toml:"sso"`
	WordPress WordPressConfig `toml:"wordpress"`
	Redis     RedisConfig     `toml:"redis"`
	Demo      DemoConfig      `toml:"demo"`
}

// DemoConfig enables read-only public-demo mode (JAB-159). When Enabled, the
// demo build's middleware short-circuits every non-idempotent /api/v1/* request
// with 403 {"error":"demo_mode"} and the /info payload carries the seeded demo
// identities so the SPA can render a DEMO banner + "Enter as admin/user" login
// buttons. The credential-exposing surface only exists in a `-tags demo` build,
// so a production binary carries none of it regardless of this config.
//
// Operator responsibility on a demo host: create the demo identities (admin +
// regular user in Kratos) and point a read-only DB user at panel-api as
// defense-in-depth beyond the write-block.
type DemoConfig struct {
	Enabled       bool   `toml:"enabled"`
	AdminEmail    string `toml:"admin_email"`
	AdminPassword string `toml:"admin_password"`
	UserEmail     string `toml:"user_email"`
	UserPassword  string `toml:"user_password"`
	// Banner is the operator-overridable copy shown in the SPA banner.
	// Empty falls back to the SPA default.
	Banner string `toml:"banner"`
}

// ServerConfig controls HTTP listener and runtime mode.
type ServerConfig struct {
	// Addr is the listen address in host:port form.
	Addr string `toml:"addr"`
	// Env is "development" or "production". Affects log format defaults
	// and strictness of Validate.
	Env string `toml:"env"`

	// TLSCert and TLSKey are paths to the certificate and private key files.
	// When both are set, the server uses ListenAndServeTLS. When empty, it
	// falls back to plain HTTP (dev mode or behind a TLS-terminating proxy).
	TLSCert string `toml:"tls_cert"`
	TLSKey  string `toml:"tls_key"`

	// Server identity and DNS nameserver configuration for hosted domains.
	// Seeded from config.toml at first boot; thereafter edited via the
	// admin Settings API and stored in the server_settings DB table.
	Hostname   string `toml:"hostname"`
	PublicIPv4 string `toml:"public_ipv4"`
	PublicIPv6 string `toml:"public_ipv6"`
	NS1Name    string `toml:"ns1_name"`
	NS1IPv4    string `toml:"ns1_ipv4"`
	NS2Name    string `toml:"ns2_name"`
	NS2IPv4    string `toml:"ns2_ipv4"`
}

// LogConfig controls slog output.
type LogConfig struct {
	// Level: debug | info | warn | error.
	Level string `toml:"level"`
	// Format: text | json. Empty means "derive from Server.Env".
	Format string `toml:"format"`
}

// DatabaseConfig holds the MariaDB DSN for the panel's own schema.
// Customer databases live elsewhere; this is only the panel's control-plane DB.
type DatabaseConfig struct {
	// URL is a MariaDB DSN, e.g. "mysql://user:pass@tcp(host:3306)/jabali_panel?parseTime=true&charset=utf8mb4&loc=UTC"
	URL string `toml:"url"`
}

// AuthConfig carries the identity-provider URLs the panel needs to
// validate session cookies. Post-M20 Kratos owns identities.
type AuthConfig struct {
	// Kratos holds Ory Kratos identity provider configuration.
	Kratos KratosConfig `toml:"kratos"`
}

// KratosConfig holds Ory Kratos URLs for identity validation.
type KratosConfig struct {
	// PublicURL is the public-facing base URL of the Kratos identity provider,
	// e.g. https://auth.example.com or http://localhost:4433 for loopback.
	// Used by the whoami client to validate session cookies.
	PublicURL string `toml:"public_url"`

	// AdminURL is the admin-facing base URL of Kratos, e.g. https://auth-admin.example.com
	// or http://localhost:4434 for loopback. Reserved for future admin operations.
	AdminURL string `toml:"admin_url"`
}

// AgentConfig holds the Unix-socket path and per-call timeout for the
// jabali-agent daemon.
type AgentConfig struct {
	SocketPath         string        `toml:"socket_path"`
	Timeout            time.Duration `toml:"timeout"`
	ReconcilerInterval time.Duration `toml:"reconciler_interval"`
}

// CORSConfig holds the SPA origin whitelist.
type CORSConfig struct {
	AllowedOrigins []string `toml:"allowed_origins"`
}

// PDNSConfig holds the PowerDNS backend database credentials for the
// reconciler to push zones and records.
type PDNSConfig struct {
	// DSN is a MySQL connection string for the jabali_pdns database,
	// e.g. "user:pass@tcp(127.0.0.1:3306)/jabali_pdns?charset=utf8mb4&parseTime=true"
	DSN string `toml:"dsn"`
}

// RedisConfig holds the Redis connection DSN for the notification
// dispatcher (ADR-0056) and future WordPress object-cache (ADR-0059).
type RedisConfig struct {
	// URL is a redis:// or unix:// DSN consumed by github.com/redis/go-redis/v9
	// via redis.ParseURL. Default on a freshly-installed host is
	// "unix:///run/redis/redis.sock?db=0" — the install.sh helper
	// install_redis writes the socket and the panel-api first-boot
	// config-seed step fills this field.
	URL string `toml:"url"`
}

// ACMEConfig holds Let's Encrypt / ACME certificate configuration.
type ACMEConfig struct {
	// StagingOnly, when true, uses Let's Encrypt's staging environment
	// (fake CA) for testing without hitting rate limits. Production
	// deployments leave this false to use the live CA.
	StagingOnly bool `toml:"staging_only"`
}

// SSOConfig holds SSO-related paths, including the envelope key for phpMyAdmin.
type SSOConfig struct {
	// KeyPath is the filesystem path to the AES-256-GCM envelope key (32 bytes).
	// Missing key is not fatal; SSO features simply unavailable.
	KeyPath string `toml:"key_path"`

	// SocketPath is the filesystem path to the Unix domain socket for SSO validation.
	// Default: /run/jabali-panel/sso.sock
	SocketPath string `toml:"socket_path"`

	// PhpMyAdminBaseURL is the full base URL for phpMyAdmin redirects.
	// Examples: "http://localhost", "https://pma.example.com"
	// When empty, defaults to http://<Host> with port stripped.
	PhpMyAdminBaseURL string `toml:"phpmyadmin_base_url"`

	// AdminerBaseURL is the full base URL for Adminer redirects
	// (engine-aware bridge for both MariaDB and PostgreSQL). Empty
	// falls back to scheme + Origin/Referer/Host like phpMyAdmin.
	AdminerBaseURL string `toml:"adminer_base_url"`
}

type WordPressConfig struct {
	// InstallTimeout is the maximum age for a WordPress install in "installing" state.
	// Default: 10 minutes
	InstallTimeout time.Duration `toml:"install_timeout"`

	// CloneTimeout is the maximum age for a WordPress install in "cloning" state.
	// Default: 30 minutes
	CloneTimeout time.Duration `toml:"clone_timeout"`

	// DeleteTimeout is the maximum age for a WordPress install in "deleting" state.
	// Default: 5 minutes
	DeleteTimeout time.Duration `toml:"delete_timeout"`

	// ProbeBatch is the maximum number of ready WordPress installs to probe per reconciler tick.
	// Default: 100
	ProbeBatch int `toml:"probe_batch"`
}

// Defaults returns a Config populated with sensible development defaults.
// Required-in-production fields (JWT secret, database URL, agent socket)
// are intentionally blank; Validate() enforces them.
func Defaults() *Config {
	return &Config{
		Server: ServerConfig{
			Addr: "127.0.0.1:8443",
			Env:  EnvDevelopment,
		},
		Log: LogConfig{
			Level:  "info",
			Format: "text", // auto-upgraded to json in production by Load()
		},
		Auth: AuthConfig{},
		Agent: AgentConfig{
			SocketPath: "/run/jabali/agent.sock",
			// 30s: generous for most commands, short enough that a wedged
			// agent doesn't hold an HTTP request hostage for minutes.
			// Per-call ctx.Deadline() overrides this when tighter.
			Timeout:            30 * time.Second,
			ReconcilerInterval: 60 * time.Second,
		},
		SSO: SSOConfig{
			KeyPath:    "/etc/jabali-panel/sso.key",
			SocketPath: "/run/jabali-panel/sso.sock",
		},
		WordPress: WordPressConfig{
			InstallTimeout: 10 * time.Minute,
			CloneTimeout:   30 * time.Minute,
			DeleteTimeout:  5 * time.Minute,
			ProbeBatch:     100,
		},
		Redis: RedisConfig{
			// unix:// DSN maps to Network:"unix" + Addr path in
			// redis.ParseURL. db=0 is the dispatcher's keyspace;
			// WordPress object-cache will use db=1 when it lands.
			URL: "unix:///run/redis/redis.sock?db=0",
		},
	}
}

// Load reads config from defaults, then (if present) the TOML file at
// tomlPath, then environment variables. An empty tomlPath skips the file
// step; a missing file is not an error.
func Load(tomlPath string) (*Config, error) {
	cfg := Defaults()

	if tomlPath != "" {
		if _, err := os.Stat(tomlPath); err == nil {
			if _, err := toml.DecodeFile(tomlPath, cfg); err != nil {
				return nil, fmt.Errorf("decode toml %q: %w", tomlPath, err)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("stat toml %q: %w", tomlPath, err)
		}
	}

	// Snapshot whether LOG_FORMAT was set via env BEFORE applying it, so we
	// can tell the difference between "user wants text in prod" and
	// "user hasn't picked a format, fall back to json".
	logFormatFromEnv := os.Getenv("LOG_FORMAT") != ""

	if err := applyEnv(cfg); err != nil {
		return nil, err
	}

	// In production, if the operator did not pick a format via env, default
	// to JSON. An explicit `format = "text"` in TOML is currently still
	// upgraded — set LOG_FORMAT=text to force plain text in production.
	if cfg.Server.Env == EnvProduction && !logFormatFromEnv && cfg.Log.Format == "text" {
		cfg.Log.Format = "json"
	}

	return cfg, nil
}

// applyEnv overlays env-var values onto cfg. Only non-empty env values apply,
// so operators can leave variables unset to inherit from file/defaults.
func applyEnv(cfg *Config) error {
	if v := os.Getenv("PANEL_ADDR"); v != "" {
		cfg.Server.Addr = v
	}
	if v := os.Getenv("PANEL_ENV"); v != "" {
		cfg.Server.Env = v
	}
	if v := os.Getenv("TLS_CERT"); v != "" {
		cfg.Server.TLSCert = v
	}
	if v := os.Getenv("TLS_KEY"); v != "" {
		cfg.Server.TLSKey = v
	}
	if v := os.Getenv("LOG_LEVEL"); v != "" {
		cfg.Log.Level = v
	}
	if v := os.Getenv("LOG_FORMAT"); v != "" {
		cfg.Log.Format = v
	}
	if v := os.Getenv("DATABASE_URL"); v != "" {
		cfg.Database.URL = v
	}
	if v := os.Getenv("KRATOS_PUBLIC_URL"); v != "" {
		cfg.Auth.Kratos.PublicURL = v
	}
	if v := os.Getenv("KRATOS_ADMIN_URL"); v != "" {
		cfg.Auth.Kratos.AdminURL = v
	}
	if v := os.Getenv("AGENT_SOCKET"); v != "" {
		cfg.Agent.SocketPath = v
	}
	if v := os.Getenv("AGENT_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("AGENT_TIMEOUT: %w", err)
		}
		cfg.Agent.Timeout = d
	}
	if v := os.Getenv("CORS_ALLOWED_ORIGINS"); v != "" {
		cfg.CORS.AllowedOrigins = splitAndTrim(v, ",")
	}
	if v := os.Getenv("JABALI_ACME_STAGING_ONLY"); v != "" {
		cfg.ACME.StagingOnly = v == "true" || v == "1"
	}
	if v := os.Getenv("JABALI_SSO_KEY_PATH"); v != "" {
		cfg.SSO.KeyPath = v
	}
	if v := os.Getenv("JABALI_SSO_SOCKET_PATH"); v != "" {
		cfg.SSO.SocketPath = v
	}
	if v := os.Getenv("WORDPRESS_INSTALL_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("WORDPRESS_INSTALL_TIMEOUT: %w", err)
		}
		cfg.WordPress.InstallTimeout = d
	}
	if v := os.Getenv("WORDPRESS_CLONE_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("WORDPRESS_CLONE_TIMEOUT: %w", err)
		}
		cfg.WordPress.CloneTimeout = d
	}
	if v := os.Getenv("WORDPRESS_DELETE_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return fmt.Errorf("WORDPRESS_DELETE_TIMEOUT: %w", err)
		}
		cfg.WordPress.DeleteTimeout = d
	}
	if v := os.Getenv("WORDPRESS_PROBE_BATCH"); v != "" {
		var batch int
		if _, err := fmt.Sscanf(v, "%d", &batch); err != nil {
			return fmt.Errorf("WORDPRESS_PROBE_BATCH: %w", err)
		}
		cfg.WordPress.ProbeBatch = batch
	}
	return nil
}

// addrPattern accepts :PORT or HOST:PORT with a limited charset.
var addrPattern = regexp.MustCompile(`^([A-Za-z0-9.\-]+)?:[0-9]{1,5}$`)

// isValidServerAddr accepts the two forms listenAndPrepare (cmd/server/
// listener.go) knows how to open:
//   - unix:/<abs-path>  → Unix-domain socket
//   - [host]:port       → TCP
//
// M25 Step 4 added the unix: form at the listener but missed extending
// the Validate hook here, so a config.toml with
// addr = "unix:/run/jabali-panel/api.sock" failed pre-flight with
// "expected [host]:port" before the listener ever got a chance.
func isValidServerAddr(addr string) bool {
	if strings.HasPrefix(addr, "unix:") {
		path := strings.TrimPrefix(addr, "unix:")
		return strings.HasPrefix(path, "/") && len(path) > 1
	}
	return addrPattern.MatchString(addr)
}

// Validate returns nil when cfg is usable; otherwise an error naming the
// specific field that's wrong. Production is strictly validated; development
// is permissive so contributors can boot without a full config.
func (c *Config) Validate() error {
	if !isValidServerAddr(c.Server.Addr) {
		return fmt.Errorf("server.addr %q: expected [host]:port or unix:/abs/path", c.Server.Addr)
	}
	if c.Server.Env != EnvDevelopment && c.Server.Env != EnvProduction {
		return fmt.Errorf("server.env %q: must be %s or %s",
			c.Server.Env, EnvDevelopment, EnvProduction)
	}

	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("log.level %q: must be one of debug|info|warn|error", c.Log.Level)
	}
	switch c.Log.Format {
	case "text", "json":
	default:
		return fmt.Errorf("log.format %q: must be text|json", c.Log.Format)
	}

	// Kratos PublicURL is required in production; dev can run without it
	// (only /health and static SPA are reachable in that state — deliberate).
	if c.Auth.Kratos.PublicURL == "" && c.Server.Env == "production" {
		return errors.New("auth.kratos.public_url: required in production")
	}

	if c.Server.Env == "production" && c.Database.URL == "" {
		return errors.New("database.url: required in production")
	}
	return nil
}

func splitAndTrim(s, sep string) []string {
	parts := strings.Split(s, sep)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
