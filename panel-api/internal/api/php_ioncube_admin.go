package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/ioncube"
)

// LoaderFetcher downloads + verifies an ionCube loader for a PHP version.
// ioncube.Fetcher satisfies it; tests inject a fake.
type LoaderFetcher interface {
	FetchLoader(ctx context.Context, version string) ([]byte, error)
}

// RegisterIonCubeAdminRoutes mounts admin-only ionCube loader management. The
// group already carries RequireAuth + RequireAdmin.
//
//	GET    /php/ioncube                         — installed + enabled + supported
//	POST   /php/ioncube/versions/:version/install
//	DELETE /php/ioncube/versions/:version
//
// panel-api owns the download (ADR-0050: the agent has no outbound HTTPS); the
// version is validated against the pinned catalogue BEFORE any fetch or socket
// call. Agent errors translate through translateAgentError (CodeFailedPrecondition
// → 409 — e.g. uninstall refused while a pool is still enabled).
func RegisterIonCubeAdminRoutes(rg *gin.RouterGroup, cli agent.AgentInterface, fetcher LoaderFetcher) {
	php := rg.Group("/php")

	php.GET("/ioncube", func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), phpVersionCallTimeout)
		defer cancel()
		raw, err := cli.Call(ctx, "php.ioncube.status", nil)
		if err != nil {
			status, body := translateAgentError(err)
			c.JSON(status, body)
			return
		}
		// Merge the live agent status with the catalogue's supported set so the
		// UI can offer "install" for versions not yet on the box.
		var status struct {
			InstalledVersions []string            `json:"installed_versions"`
			Enabled           map[string][]string `json:"enabled"`
		}
		_ = json.Unmarshal(raw, &status)
		if status.InstalledVersions == nil {
			status.InstalledVersions = []string{}
		}
		if status.Enabled == nil {
			status.Enabled = map[string][]string{}
		}
		c.JSON(http.StatusOK, gin.H{
			"installed_versions": status.InstalledVersions,
			"enabled":            status.Enabled,
			"supported_versions": ioncube.SupportedVersions(),
			"loader_version":     ioncube.LoaderVersion,
		})
	})

	php.POST("/ioncube/versions/:version/install", func(c *gin.Context) {
		version := c.Param("version")
		if !ioncube.ValidVersion(version) {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid_argument", "detail": "ionCube loader not offered for this PHP version"})
			return
		}
		sum, err := ioncube.SHA256For(version)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid_argument", "detail": err.Error()})
			return
		}
		// Fetch + verify against the catalogue (panel-api side).
		fetchCtx, fcancel := context.WithTimeout(c.Request.Context(), ioncubeFetchTimeout)
		defer fcancel()
		data, err := fetcher.FetchLoader(fetchCtx, version)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"status": "error", "error": "loader_fetch_failed", "detail": err.Error()})
			return
		}
		// Hand the verified bytes to the agent, which re-verifies + installs.
		ctx, cancel := context.WithTimeout(c.Request.Context(), adminActionTimeout)
		defer cancel()
		raw, err := cli.Call(ctx, "php.ioncube.install", map[string]string{
			"version": version,
			"sha256":  sum,
			"so_b64":  base64.StdEncoding.EncodeToString(data),
		})
		if err != nil {
			status, body := translateAgentError(err)
			c.JSON(status, body)
			return
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
	})

	php.DELETE("/ioncube/versions/:version", func(c *gin.Context) {
		version := c.Param("version")
		if !ioncube.ValidVersion(version) {
			c.JSON(http.StatusBadRequest, gin.H{"status": "error", "error": "invalid_argument", "detail": "ionCube loader not offered for this PHP version"})
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), adminActionTimeout)
		defer cancel()
		raw, err := cli.Call(ctx, "php.ioncube.uninstall", map[string]string{"version": version})
		if err != nil {
			status, body := translateAgentError(err)
			c.JSON(status, body)
			return
		}
		c.Data(http.StatusOK, "application/json; charset=utf-8", raw)
	})
}
