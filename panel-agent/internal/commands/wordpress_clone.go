package commands

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

// wordpressCloneReq is the input shape for wordpress.clone.
type wordpressCloneReq struct {
	OSUser         string `json:"os_user"`           // domain owner (e.g. "shuki")
	SrcDocroot     string `json:"src_docroot"`       // source docroot
	DstDocroot     string `json:"dst_docroot"`       // destination docroot
	SrcDBName      string `json:"src_db_name"`       // source database name
	DstDBName      string `json:"dst_db_name"`       // destination database name
	DstDBUser      string `json:"dst_db_user"`       // destination database user
	DstDBPassword  string `json:"dst_db_password"`   // destination database password
	DstDBHost      string `json:"dst_db_host"`       // destination database host
	SrcSiteURL     string `json:"src_site_url"`      // source site URL (e.g., https://example.com)
	DstSiteURL     string `json:"dst_site_url"`      // destination site URL
	UseWWW         bool   `json:"use_www"`           // prepend www. to domain in siteurl
	DstSubdirectory string `json:"dst_subdirectory"` // destination subdirectory (optional)
}

// wordpressCloneResp is the output shape for wordpress.clone.
type wordpressCloneResp struct {
	Version string `json:"version"` // WordPress version on the cloned install
}

// NOTE: Clone is non-idempotent and non-retryable in place.
// If rsync succeeds but search-replace fails, the handler returns the error.
// The API caller (Step 4) is responsible for marking the row failed and the user does delete + retry.
// This means if rsync succeeds but search-replace fails, retrying the same command will re-rsync
// (harmless) and fail again on search-replace. Recovery: the API handler marks row failed,
// the user deletes the dst install (which tears down its DB + docroot contents), and retries from scratch.

func wordpressCloneHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var req wordpressCloneReq
	if err := json.Unmarshal(params, &req); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("failed to parse params: %v", err),
		}
	}

	// Validate required fields
	if req.OSUser == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "os_user is required",
		}
	}
	if req.SrcDocroot == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "src_docroot is required",
		}
	}
	if req.DstDocroot == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "dst_docroot is required",
		}
	}
	if req.SrcDBName == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "src_db_name is required",
		}
	}
	if req.DstDBName == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "dst_db_name is required",
		}
	}
	if req.DstDBUser == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "dst_db_user is required",
		}
	}
	if req.DstDBPassword == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "dst_db_password is required",
		}
	}
	if req.DstDBHost == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "dst_db_host is required",
		}
	}
	if req.SrcSiteURL == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "src_site_url is required",
		}
	}
	if req.DstSiteURL == "" {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: "dst_site_url is required",
		}
	}

	// Validate both docroots are within /home/<osuser>/domains/
	if err := validateDocrootPath(req.OSUser, req.SrcDocroot); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid src_docroot: %v", err),
		}
	}
	if err := validateDocrootPath(req.OSUser, req.DstDocroot); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInvalidArgument,
			Message: fmt.Sprintf("invalid dst_docroot: %v", err),
		}
	}


	// Compute dstInstallPath: dst_docroot + optional subdirectory
	dstInstallPath := req.DstDocroot
	if req.DstSubdirectory != "" {
		dstInstallPath = filepath.Join(dstInstallPath, req.DstSubdirectory)
		// Create the subdirectory if it doesn't exist
		mkdirCmd := buildSystemdRunCmd(ctx,
			req.OSUser,
			"mkdir", "-p", dstInstallPath,
		)
		if err := mkdirCmd.Run(); err != nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("failed to create subdirectory: %v", err),
			}
		}
	}

	// Note: trailing slash on both src and dst
	rsyncSrc := req.SrcDocroot + "/"
	rsyncDst := dstInstallPath + "/"

	rsyncCmd := buildSystemdRunCmd(ctx,
		req.OSUser,
		"rsync", "-a", "--delete", rsyncSrc, rsyncDst,
	)
	if err := rsyncCmd.Run(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("rsync failed: %v", err),
		}
	}

	// Step 2: mysqldump --single-transaction <src_db> | mysql <dst_db>
	// Dump runs as the panel's DB admin user (read from agent's env, same path M7 uses)
	// Restore runs as the same user. This avoids embedding user DB passwords in argv.
	//
	// We need to construct the mysqldump and mysql commands and pipe between them.
	// The agent process should already have access to the DB admin credentials via env or config.
	// For now, we'll assume the agent has access and use the host/port from the environment
	// or defaults to localhost (unix socket).

	dumpCmd := execCommandContext(ctx,
		"mysqldump",
		"--single-transaction",
		req.SrcDBName,
	)

	restoreCmd := execCommandContext(ctx,
		"mysql",
		"-h", req.DstDBHost,
		req.DstDBName,
	)

	// Pipe dump to restore
	pipe, err := dumpCmd.StdoutPipe()
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to create dump pipe: %v", err),
		}
	}

	restoreCmd.Stdin = pipe

	// Capture restore stderr for error reporting
	var restoreErr bytes.Buffer
	restoreCmd.Stderr = &restoreErr

	// Start the restore command first so it's ready to read
	if err := restoreCmd.Start(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to start mysql restore: %v", err),
		}
	}

	// Run the dump command
	if err := dumpCmd.Run(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("mysqldump failed: %v", err),
		}
	}

	// Close the pipe
	pipe.Close()

	// Wait for the restore to complete
	if err := restoreCmd.Wait(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("mysql restore failed: %v; stderr: %s", err, restoreErr.String()),
		}
	}

	// Step 3: Rewrite <dst_docroot>/wp-config.php via wp config set
	// Apply the same credential-handling invariant: no plaintext DB password in argv.
	// Instead, we'll use the __JABALI_PLACEHOLDER__ pattern and rewrite in-process.

	configPath := filepath.Join(dstInstallPath, "wp-config.php")

	// First, run wp config set for DB_NAME, DB_USER, DB_HOST (don't set password via argv)
	configSetCmd := buildSystemdRunCmd(ctx,
		req.OSUser,
		"wp", "config", "set",
		"DB_NAME", req.DstDBName,
		"--path="+dstInstallPath,
		"--type=constant",
	)
	if err := configSetCmd.Run(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("wp config set DB_NAME failed: %v", err),
		}
	}

	configSetCmd = buildSystemdRunCmd(ctx,
		req.OSUser,
		"wp", "config", "set",
		"DB_USER", req.DstDBUser,
		"--path="+dstInstallPath,
		"--type=constant",
	)
	if err := configSetCmd.Run(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("wp config set DB_USER failed: %v", err),
		}
	}

	configSetCmd = buildSystemdRunCmd(ctx,
		req.OSUser,
		"wp", "config", "set",
		"DB_HOST", req.DstDBHost,
		"--path="+dstInstallPath,
		"--type=constant",
	)
	if err := configSetCmd.Run(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("wp config set DB_HOST failed: %v", err),
		}
	}

	// Now handle DB_PASSWORD by rewriting the config file (same pattern as wordpress_install.go)
	// Read the config file and replace the password placeholder with the real password
	configContent, err := readFileAsUser(ctx, req.OSUser, configPath)
	if err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to read wp-config.php: %v", err),
		}
	}

	// GH #621: strip the source's JABALI_CACHE_* block. It pins the SOURCE
	// install's cache identity (prefix = <osuser>:<sourceInstallID>, ACL user,
	// token), so a verbatim copy would make the clone's object-cache drop-in
	// read/write the SOURCE's Redis namespace — cross-install cache bleed, and a
	// flush on one clearing the other. Removing it leaves the clone with a cold,
	// fresh cache (the drop-in re-derives a distinct prefix and, lacking the ACL
	// creds, fails safe to no-persistent-cache) until the panel re-enables it,
	// which re-stamps fresh constants + provisions the clone's own ACL scope.
	// The clone's install row is created with cache_enabled=false to match.
	configContent = stripWPCacheBlock(configContent)

	// Replace or set DB_PASSWORD: look for define( 'DB_PASSWORD', ... );
	// If not found, we add it. If found, we update it.
	if strings.Contains(configContent, "define( 'DB_PASSWORD'") {
		// Replace existing definition
		configContent = replaceWordPressConstant(configContent, "DB_PASSWORD", req.DstDBPassword)
	} else {
		// Add new definition after DB_USER
		configContent = strings.ReplaceAll(
			configContent,
			"define( 'DB_USER',",
			"define( 'DB_PASSWORD', '"+escapeWordPressConstant(req.DstDBPassword)+"' );\ndefine( 'DB_USER',",
		)
	}

	// Write back with restricted permissions (0640)
	if err := writeFileAsUser(ctx, req.OSUser, configPath, configContent, 0640); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("failed to write wp-config.php: %v", err),
		}
	}

	// Step 4: wp search-replace --path=<dst> --all-tables <src_site_url> <dst_site_url>
	searchReplaceCmd := buildSystemdRunCmd(ctx,
		req.OSUser,
		"wp", "search-replace",
		req.SrcSiteURL, req.DstSiteURL,
		"--path="+dstInstallPath,
		"--all-tables",
	)
	if err := searchReplaceCmd.Run(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("wp search-replace failed: %v", err),
		}
	}

	// Step 5: wp core version --path=<dst> to capture and return version
	versionCmd := buildSystemdRunCmd(ctx,
		req.OSUser,
		"wp", "core", "version",
		"--path="+dstInstallPath,
	)

	var versionOutput bytes.Buffer
	versionCmd.Stdout = &versionOutput
	versionCmd.Stderr = io.Discard

	if err := versionCmd.Run(); err != nil {
		return nil, &agentwire.AgentError{
			Code:    agentwire.CodeInternal,
			Message: fmt.Sprintf("wp core version failed: %v", err),
		}
	}

	version := strings.TrimSpace(versionOutput.String())

	return wordpressCloneResp{
		Version: version,
	}, nil
}

// Helper functions for in-process wp-config.php rewriting
// (avoiding plaintext passwords in argv)

// readFileAsUser reads a file as the given user via cat in systemd-run
func readFileAsUser(ctx context.Context, osUser, filePath string) (string, error) {
	cmd := buildSystemdRunCmd(ctx, osUser, "cat", filePath)
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return "", err
	}
	return out.String(), nil
}

// writeFileAsUser writes a file as the given user via tee in systemd-run
func writeFileAsUser(ctx context.Context, osUser, filePath, content string, perm uint32) error {
	// Use tee to write the file and chown simultaneously
	cmd := buildSystemdRunCmd(ctx, osUser, "tee", filePath)
	cmd.Stdin = strings.NewReader(content)
	cmd.Stdout = io.Discard
	if err := cmd.Run(); err != nil {
		return err
	}

	// Chown to the user (required because tee inherits umask)
	chownCmd := execCommandContext(ctx, "chown", osUser+":"+osUser, filePath)
	if err := chownCmd.Run(); err != nil {
		return err
	}

	// Set permissions
	chmodCmd := execCommandContext(ctx, "chmod", fmt.Sprintf("%o", perm), filePath)
	if err := chmodCmd.Run(); err != nil {
		return err
	}

	return nil
}

// replaceWordPressConstant replaces a WordPress constant value in wp-config.php
func replaceWordPressConstant(config, name, value string) string {
	// Pattern: define( 'DB_PASSWORD', 'old_value' );
	// Replace with: define( 'DB_PASSWORD', 'new_value' );
	escaped := escapeWordPressConstant(value)

	// Find the line and replace it
	lines := strings.Split(config, "\n")
	for i, line := range lines {
		if strings.Contains(line, "define( '"+name+"'") {
			// Replace this line
			lines[i] = `define( '` + name + `', '` + escaped + `' );`
			break
		}
	}
	return strings.Join(lines, "\n")
}

// escapeWordPressConstant escapes a value for use in a WordPress constant
func escapeWordPressConstant(value string) string {
	// Escape single quotes
	value = strings.ReplaceAll(value, "'", "\\'")
	return value
}

func init() {
	Default.Register("wordpress.clone", wordpressCloneHandler)
	RegisterAppCloner("wordpress", wordpressCloneHandler)
}
