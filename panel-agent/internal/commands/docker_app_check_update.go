// docker_app.check_update — query the upstream registry for the
// current digest of an image, compare against the on-disk digest
// of the running container. Cheap (~1 HTTP HEAD via `docker manifest
// inspect`); called by the reconciler every ~6h per app.
package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"git.jabali-panel.com/shukivaknin/jabali2/agentwire"
)

type dockerAppCheckUpdateParams struct {
	Slug         string `json:"slug"`
	ImageChannel string `json:"image_channel"` // e.g. "vaultwarden/server:latest"
}

type dockerAppCheckUpdateResponse struct {
	Slug            string `json:"slug"`
	ImageChannel    string `json:"image_channel"`
	CurrentDigest   string `json:"current_digest,omitempty"`   // local container digest
	AvailableDigest string `json:"available_digest,omitempty"` // upstream registry digest
	UpdateAvailable bool   `json:"update_available"`
}

func dockerAppCheckUpdateHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p dockerAppCheckUpdateParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: fmt.Sprintf("parse: %v", err)}
	}
	if err := validateSlug(p.Slug); err != nil {
		return nil, err
	}
	if p.ImageChannel == "" {
		return nil, &agentwire.AgentError{Code: agentwire.CodeInvalidArgument, Message: "image_channel required"}
	}

	// Target digest. When the catalog pins the image (repo:tag@sha256:<digest>,
	// the norm post-#458), the reviewed target IS the embedded digest — compare
	// the running container against THAT, not the mutable tag's current registry
	// digest. This also avoids a registry round-trip and an index-vs-per-arch
	// digest mismatch that `docker manifest inspect --verbose` would introduce.
	// Legacy un-pinned entries fall back to resolving the tag's current digest.
	var upstream string
	if i := strings.Index(p.ImageChannel, "@sha256:"); i >= 0 {
		upstream = p.ImageChannel[i+1:]
	} else {
		var derr error
		upstream, derr = dockerManifestDigest(ctx, p.ImageChannel)
		if derr != nil {
			return nil, &agentwire.AgentError{
				Code:    agentwire.CodeInternal,
				Message: fmt.Sprintf("docker manifest inspect failed: %v", derr),
			}
		}
	}

	// Local digest: the container's currently-running image.
	// docker inspect --format '{{.Image}}' on the container, then
	// docker image inspect to resolve a content digest. Simpler:
	// docker inspect jabali-app-<slug> --format '{{index .Config.Image}}'
	// + image inspect for the RepoDigests[0].
	local := dockerContainerImageDigest(ctx, "jabali-app-"+p.Slug)

	resp := dockerAppCheckUpdateResponse{
		Slug:            p.Slug,
		ImageChannel:    p.ImageChannel,
		CurrentDigest:   local,
		AvailableDigest: upstream,
		UpdateAvailable: upstream != "" && local != "" && upstream != local,
	}
	return resp, nil
}

func dockerManifestDigest(ctx context.Context, image string) (string, error) {
	out, err := execCommandContext(ctx, "docker", "manifest", "inspect", "--verbose", image).Output()
	if err != nil {
		// Try without --verbose for older docker.
		out, err = execCommandContext(ctx, "docker", "manifest", "inspect", image).Output()
		if err != nil {
			return "", err
		}
	}
	// --verbose returns either a single object or a list (multi-arch
	// manifests). Walk it and prefer linux/amd64.
	var arr []map[string]any
	if err := json.Unmarshal(out, &arr); err == nil {
		for _, m := range arr {
			if desc, ok := m["Descriptor"].(map[string]any); ok {
				if d, ok := desc["digest"].(string); ok && d != "" {
					return d, nil
				}
			}
		}
	}
	// Single-arch fallback (non-verbose shape).
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err == nil {
		if d, ok := obj["Digest"].(string); ok && d != "" {
			return d, nil
		}
	}
	return "", nil
}

func dockerContainerImageDigest(ctx context.Context, container string) string {
	// 1. Resolve the image ID the container is running.
	img, err := execCommandContext(ctx, "docker", "inspect", "--format", "{{.Image}}", container).Output()
	if err != nil {
		return ""
	}
	imageID := strings.TrimSpace(string(img))
	if imageID == "" {
		return ""
	}
	// 2. Read RepoDigests off that image. The first one is the
	// authoritative remote digest.
	out, err := execCommandContext(ctx, "docker", "image", "inspect", "--format", "{{json .RepoDigests}}", imageID).Output()
	if err != nil {
		return ""
	}
	var arr []string
	if err := json.Unmarshal(out, &arr); err != nil || len(arr) == 0 {
		return ""
	}
	// repoDigest shape: "vaultwarden/server@sha256:abcdef...". Split.
	parts := strings.SplitN(arr[0], "@", 2)
	if len(parts) == 2 {
		return parts[1]
	}
	return ""
}

func init() {
	Default.Register("docker_app.check_update", dockerAppCheckUpdateHandler)
}
