package commands

// M46 Step 6 — show DB processes + kill (ADR-0100). Root-over-socket
// (MariaDB) / postgres-over-peer (Postgres); jabali_panel gains no
// PROCESS/SUPER. KILL / pg_terminate_backend are audited by panel-api.

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

type dbProc struct {
	ID      string `json:"id"`
	User    string `json:"user"`
	Host    string `json:"host"`
	DB      string `json:"db"`
	Command string `json:"command"`
	Time    string `json:"time"`
	State   string `json:"state"`
	Info    string `json:"info"`
}

type dbProcListResponse struct {
	Processes []dbProc `json:"processes"`
}

type dbKillParams struct {
	ID string `json:"id"`
}

type dbKillResponse struct {
	OK bool `json:"ok"`
}

func splitTabRows(out string) [][]string {
	var rows [][]string
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		rows = append(rows, strings.Split(line, "\t"))
	}
	return rows
}

func cell(r []string, i int) string {
	if i < len(r) {
		return r[i]
	}
	return ""
}

// dbProcessListHandler — MariaDB, via information_schema.PROCESSLIST
// (stable columns; -B -N gives tab-separated, header-less rows).
func dbProcessListHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	const q = "SELECT Id,User,Host,IFNULL(db,''),Command,Time,IFNULL(State,'')," +
		"IFNULL(LEFT(Info,200),'') FROM information_schema.PROCESSLIST ORDER BY Time DESC"
	out, err := execCommandContext(ctx, "mysql", "-N", "-B", "-e", q).CombinedOutput()
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "processlist query failed"}
	}
	procs := []dbProc{}
	for _, r := range splitTabRows(string(out)) {
		procs = append(procs, dbProc{
			ID: cell(r, 0), User: cell(r, 1), Host: cell(r, 2), DB: cell(r, 3),
			Command: cell(r, 4), Time: cell(r, 5), State: cell(r, 6), Info: cell(r, 7),
		})
	}
	return dbProcListResponse{Processes: procs}, nil
}

// dbKillHandler — MariaDB KILL <id>. id must be a positive integer.
func dbKillHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dbKillParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	n, err := strconv.ParseUint(p.ID, 10, 64)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "id must be a positive integer"}
	}
	if err := execCommandContext(ctx, "mysql", "-e", fmt.Sprintf("KILL %d", n)).Run(); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "kill failed"}
	}
	return dbKillResponse{OK: true}, nil
}

// dbPostgresActivityHandler — pg_stat_activity (excludes our own
// backend). Tab-separated via -F.
func dbPostgresActivityHandler(ctx context.Context, _ json.RawMessage) (any, error) {
	const q = "SELECT pid, usename, COALESCE(client_addr::text,'local'), COALESCE(datname,''), " +
		"state, COALESCE(left(query,200),'') FROM pg_stat_activity " +
		"WHERE pid <> pg_backend_pid() ORDER BY state"
	cmd := execCommandContext(ctx, "sudo", "-u", "postgres", "psql",
		"-v", "ON_ERROR_STOP=1", "-XAtq", "-F", "\t", "-c", q)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "pg_stat_activity query failed"}
	}
	procs := []dbProc{}
	for _, r := range splitTabRows(string(out)) {
		procs = append(procs, dbProc{
			ID: cell(r, 0), User: cell(r, 1), Host: cell(r, 2), DB: cell(r, 3),
			Command: "", Time: "", State: cell(r, 4), Info: cell(r, 5),
		})
	}
	return dbProcListResponse{Processes: procs}, nil
}

// dbPostgresTerminateHandler — pg_terminate_backend(pid).
func dbPostgresTerminateHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dbKillParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse params: %v", err)}
	}
	n, err := strconv.ParseUint(p.ID, 10, 64)
	if err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "pid must be a positive integer"}
	}
	if err := pgRunSQL(ctx, fmt.Sprintf("SELECT pg_terminate_backend(%d)", n)); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInternal, Message: "terminate failed"}
	}
	return dbKillResponse{OK: true}, nil
}

func init() {
	Default.Register("db.processlist", dbProcessListHandler)
	Default.Register("db.kill", dbKillHandler)
	Default.Register("db.postgres.activity", dbPostgresActivityHandler)
	Default.Register("db.postgres.terminate", dbPostgresTerminateHandler)
}
