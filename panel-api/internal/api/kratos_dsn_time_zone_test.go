package api

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/stretchr/testify/require"
)

// Kratos keeps recovery and verification code times in TIMESTAMP columns,
// which MariaDB converts with the session time zone, and it sends them in
// UTC. Its session must be UTC too: following the server's zone, a
// time-zone change made every outstanding code invalid (moving west) or last
// longer (moving east).
func TestKratosDSNPinsUTC(t *testing.T) {
	// panel-api/internal/api → repo root is ../../../
	raw, err := os.ReadFile("../../../install/kratos.yml.tmpl")
	require.NoError(t, err)
	m := regexp.MustCompile(`(?m)^dsn: "mysql://([^"]+)"$`).FindStringSubmatch(string(raw))
	require.Len(t, m, 2, "dsn line not found in kratos.yml.tmpl")
	dsn := strings.NewReplacer(
		"{{.KratosDatabaseUser}}", "u",
		"{{.KratosDatabasePassword}}", "p",
		"{{.KratosDatabaseName}}", "db",
	).Replace(m[1])
	cfg, err := mysql.ParseDSN(dsn)
	require.NoError(t, err)
	require.Equal(t, "'+00:00'", cfg.Params["time_zone"],
		"Kratos's MariaDB session must be pinned to UTC")
}
