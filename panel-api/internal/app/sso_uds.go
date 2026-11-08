package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/user"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/api"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/middleware"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/repository"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/sso"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ssokey"
)

// ssoUDSBodyLimitBytes caps a validate request body. The body is one token
// ({"token":"<base64url>"}), so this is far above any real request.
const ssoUDSBodyLimitBytes int64 = 16 << 10

// StartSSOUDSListener starts a Unix domain socket HTTP server for phpMyAdmin
// SSO validation. The listener binds to socketPath with mode 0660 and
// owner jabali:www-data. It handles POST /sso/phpmyadmin/validate without
// JWT auth; the socket ACL is the boundary.
// The server runs in the background; callers must call the returned cancel func to gracefully shutdown.
// Stale socket files are removed before binding.
func StartSSOUDSListener(
	socketPath string,
	databases repository.DatabaseRepository,
	users repository.UserRepository,
	tokens repository.PhpMyAdminSSOTokenRepository,
	adminerTokens repository.AdminerSSOTokenRepository,
	adminerSSO *sso.AdminerService,
	ssoKey *ssokey.Key,
	log *slog.Logger,
) (*http.Server, func(context.Context) error, error) {
	if socketPath == "" {
		return nil, nil, errors.New("socketPath is required")
	}

	// Remove stale socket file if it exists
	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, fmt.Errorf("remove stale socket: %w", err)
	}

	// Create the router
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(middleware.BodyLimit(ssoUDSBodyLimitBytes, nil))

	cfg := api.SSOPhpMyAdminValidateHandlerConfig{
		Databases: databases,
		Users:     users,
		Tokens:    tokens,
		SSOKey:    ssoKey,
		Log:       log,
	}
	g := r.Group("")
	api.RegisterSSOPhpMyAdminValidateRoutes(g, cfg)
	// M37 Phase 4: Adminer validate route on the same UDS — Adminer's
	// jabali-sso plugin POSTs to /sso/adminer/validate; permission
	// model identical to phpMyAdmin (group=www-data, mode 0660).
	if adminerSSO != nil && adminerTokens != nil {
		api.RegisterSSOAdminerValidateRoutes(g, api.SSOAdminerValidateHandlerConfig{
			Databases: databases,
			Users:     users,
			Tokens:    adminerTokens,
			Adminer:   adminerSSO,
			Log:       log,
		})
	}

	// Create Unix domain socket listener
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, nil, fmt.Errorf("listen on unix socket: %w", err)
	}

	// Socket perms: mode 0660, group www-data. phpMyAdmin's sso.php runs
	// inside the jabali-pma FPM pool as www-data and needs to stream_socket_client()
	// into this UDS. The panel process runs as jabali (owner by default),
	// so setting group=www-data with 0660 lets both sides open the socket
	// while blocking other hosting users. If the www-data group does not
	// exist (uncommon minimal images), fall back to 0660 with default group
	// and log a warning — SSO will fail closed until the operator fixes it.
	if err := os.Chmod(socketPath, 0660); err != nil {
		listener.Close()
		return nil, nil, fmt.Errorf("chmod socket: %w", err)
	}
	if grp, gErr := user.LookupGroup("www-data"); gErr == nil {
		gid, convErr := strconv.Atoi(grp.Gid)
		if convErr == nil {
			if err := os.Chown(socketPath, -1, gid); err != nil {
				log.Warn("SSO UDS chown to www-data failed; phpMyAdmin will see Permission denied", "socket", socketPath, "err", err)
			}
		}
	} else {
		log.Warn("www-data group not found; phpMyAdmin SSO will fail until group is created", "socket", socketPath)
	}

	// Create HTTP server
	srv := &http.Server{
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       90 * time.Second,
	}

	// Start server in background
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- srv.Serve(listener)
	}()

	// Monitor for serve errors
	go func() {
		select {
		case err := <-serveErr:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("SSO UDS listener error", "err", err)
			}
		}
	}()

	log.Info("SSO UDS listener started", "socket", socketPath)

	// Return server and a shutdown function
	shutdown := func(shutdownCtx context.Context) error {
		if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("shutdown UDS server: %w", err)
		}
		// Remove socket file on shutdown
		if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Warn("remove socket on shutdown", "err", err)
		}
		log.Info("SSO UDS listener stopped")
		return nil
	}

	return srv, shutdown, nil
}
