package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/phpext"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ioncube"
)

// ExtensionApplyRequest binds the POST body for the apply endpoint. The
// allowed action set matches the agent's php.ext.apply contract and is
// locked in panel-api/internal/agent/php_ext_contract_test.go.
type ExtensionApplyRequest struct {
	Action string `json:"action" binding:"required,oneof=install remove enable disable"`
}

// LoaderFetcher downloads + verifies an ionCube loader for a PHP version.
// ioncube.Fetcher satisfies it; tests inject a fake.
type LoaderFetcher interface {
	FetchLoader(ctx context.Context, version string) ([]byte, error)
}

// extRow mirrors the agent's php.ext.list row shape so the ionCube pseudo-row
// slots into the same table the SPA already renders.
type extRow struct {
	Name      string `json:"name"`
	Installed bool   `json:"installed"`
	Enabled   bool   `json:"enabled"`
	BuiltIn   bool   `json:"built_in"`
}

type extListEnvelope struct {
	Version    string   `json:"version"`
	Extensions []extRow `json:"extensions"`
}

// RegisterPHPExtensionAdminRoutes mounts admin-only PHP extension management
// routes. The group is expected to already carry RequireAuth + RequireAdmin.
//
// GET  /php/versions/:version/extensions
// POST /php/versions/:version/extensions/:ext/apply
//
// The dpkg-backed extensions come straight from the agent's php.ext.* (ADR-0031,
// "live from dpkg"). ionCube is NOT a dpkg package — it's the closed loader
// jabali fetches + installs — so it is composed in HERE as a pseudo-row and its
// apply actions route to the ionCube RPCs, keeping the agent's ext path pure.
// version + ext are validated before the agent is called; unknown values never
// reach the socket. Agent errors translate through translateAgentError
// (CodeFailedPrecondition -> 409).
func RegisterPHPExtensionAdminRoutes(rg *gin.RouterGroup, cli agent.AgentInterface, fetcher LoaderFetcher) {
	php := rg.Group("/php")

	php.GET("/versions/:version/extensions", func(c *gin.Context) {
		version := c.Param("version")
		if !phpext.ValidVersion(version) {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid_argument", "detail": "invalid version format"})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), phpVersionCallTimeout)
		defer cancel()
		raw, err := cli.Call(ctx, "php.ext.list", map[string]string{"version": version})
		if err != nil {
			status, body := translateAgentError(err)
			c.JSON(status, body)
			return
		}
		// Compose the ionCube pseudo-row for versions ionCube supports. Best-
		// effort: a status hiccup just omits the row rather than failing the
		// whole extension list.
		if ioncube.ValidVersion(version) {
			if merged, ok := appendIonCubeRow(ctx, cli, version, raw); ok {
				c.Data(http.StatusOK, "application/json; charset=utf-8", merged)
				return
			}
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
	})

	php.POST("/versions/:version/extensions/:ext/apply", func(c *gin.Context) {
		version := c.Param("version")
		ext := c.Param("ext")
		if !phpext.ValidVersion(version) {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid_argument", "detail": "invalid version format"})
			return
		}
		var req ExtensionApplyRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid_argument", "detail": err.Error()})
			return
		}

		// ionCube is not a dpkg extension — route its actions to the loader RPCs.
		if ext == "ioncube" {
			applyIonCube(c, cli, fetcher, version, req.Action)
			return
		}

		if _, ok := phpext.Lookup(ext); !ok {
			c.JSON(http.StatusNotFound, gin.H{"status": "error", "error": "not_found", "detail": "unknown extension"})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), adminActionTimeout)
		defer cancel()
		raw, err := cli.Call(ctx, "php.ext.apply", map[string]string{
			"version": version,
			"ext":     ext,
			"action":  req.Action,
		})
		if err != nil {
			status, body := translateAgentError(err)
			c.JSON(status, body)
			return
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
	})
}

// appendIonCubeRow reads live ionCube status and appends an "ioncube" row to the
// php.ext.list payload. Returns (merged, true) on success; (nil, false) if the
// status call or JSON handling fails (caller falls back to the raw list).
func appendIonCubeRow(ctx context.Context, cli agent.AgentInterface, version string, raw []byte) ([]byte, bool) {
	var env extListEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, false
	}
	statusRaw, err := cli.Call(ctx, "php.ioncube.status", nil)
	if err != nil {
		return nil, false
	}
	var status struct {
		InstalledVersions []string `json:"installed_versions"`
		EnabledVersions   []string `json:"enabled_versions"`
	}
	if err := json.Unmarshal(statusRaw, &status); err != nil {
		return nil, false
	}
	row := extRow{Name: "ioncube", BuiltIn: false}
	for _, v := range status.InstalledVersions {
		if v == version {
			row.Installed = true
		}
	}
	for _, v := range status.EnabledVersions {
		if v == version {
			row.Enabled = true
		}
	}
	env.Extensions = append(env.Extensions, row)
	merged, err := json.Marshal(env)
	if err != nil {
		return nil, false
	}
	return merged, true
}

// applyIonCube routes an ionCube ext-apply action to the loader RPCs. install
// fetches + verifies the loader panel-side (ADR-0050) then hands bytes to the
// agent; enable/disable flip the server-wide conf.d; remove uninstalls the .so.
func applyIonCube(c *gin.Context, cli agent.AgentInterface, fetcher LoaderFetcher, version, action string) {
	if !ioncube.ValidVersion(version) {
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid_argument", "detail": "ionCube loader not offered for this PHP version"})
		return
	}
	switch action {
	case "install":
		sum, err := ioncube.SHA256For(version)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid_argument", "detail": err.Error()})
			return
		}
		fetchCtx, fcancel := context.WithTimeout(c.Request.Context(), ioncubeFetchTimeout)
		defer fcancel()
		data, err := fetcher.FetchLoader(fetchCtx, version)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"status": "error", "error": "loader_fetch_failed", "detail": err.Error()})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), adminActionTimeout)
		defer cancel()
		callIonCube(c, cli, ctx, "php.ioncube.install", map[string]string{
			"version": version, "sha256": sum, "so_b64": base64.StdEncoding.EncodeToString(data),
		})
	case "remove":
		ctx, cancel := context.WithTimeout(c.Request.Context(), adminActionTimeout)
		defer cancel()
		callIonCube(c, cli, ctx, "php.ioncube.uninstall", map[string]string{"version": version})
	case "enable", "disable":
		ctx, cancel := context.WithTimeout(c.Request.Context(), adminActionTimeout)
		defer cancel()
		callIonCube(c, cli, ctx, "php.ioncube.server_set", map[string]any{
			"version": version, "enabled": action == "enable",
		})
	default:
		c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid_argument", "detail": "unknown action"})
	}
}

func callIonCube(c *gin.Context, cli agent.AgentInterface, ctx context.Context, cmd string, params any) {
	raw, err := cli.Call(ctx, cmd, params)
	if err != nil {
		status, body := translateAgentError(err)
		c.JSON(status, body)
		return
	}
	c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
}
