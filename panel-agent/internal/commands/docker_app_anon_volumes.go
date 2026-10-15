package commands

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
)

// anonymousVolumeLabel marks a volume Docker made for an image VOLUME path
// that has no named volume or bind mount over it.
const anonymousVolumeLabel = "com.docker.volume.anonymous"

// dockerVolumeNameRe is Docker's volume name charset. A name read back from
// docker is passed to `docker volume rm` as an argument; one that could read
// as a flag is never passed.
var dockerVolumeNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,254}$`)

// composeAnonymousVolumes lists the anonymous volumes the containers of the
// compose project in dir mount. `docker compose down -v` removes an anonymous
// volume only with the container that made it. A recreate (an update, an
// edit, a changed env) mounts the old container's anonymous volumes into the
// new container by name, so a delete left them on disk with whatever the app
// wrote to those paths. List them before `down` removes the containers.
// Best effort: a docker error lists nothing.
func composeAnonymousVolumes(ctx context.Context, dir string) []string {
	out, err := composeCommand(ctx, dir, "ps", "-a", "-q").Output()
	if err != nil {
		return nil
	}
	ids := strings.Fields(string(out))
	if len(ids) == 0 {
		return nil
	}
	out, err = execCommandContext(ctx, "docker", append([]string{"inspect", "--type", "container"}, ids...)...).Output()
	if err != nil {
		return nil
	}
	var containers []struct {
		Mounts []struct {
			Type string
			Name string
		}
	}
	if json.Unmarshal(out, &containers) != nil {
		return nil
	}
	mounted := map[string]bool{}
	var names []string
	for _, c := range containers {
		for _, m := range c.Mounts {
			if m.Type == "volume" && dockerVolumeNameRe.MatchString(m.Name) && !mounted[m.Name] {
				mounted[m.Name] = true
				names = append(names, m.Name)
			}
		}
	}
	if len(names) == 0 {
		return nil
	}
	out, err = execCommandContext(ctx, "docker", append([]string{"volume", "inspect"}, names...)...).Output()
	if err != nil {
		return nil
	}
	var vols []struct {
		Name   string
		Labels map[string]string
	}
	if json.Unmarshal(out, &vols) != nil {
		return nil
	}
	var anon []string
	for _, v := range vols {
		if _, ok := v.Labels[anonymousVolumeLabel]; ok && mounted[v.Name] {
			anon = append(anon, v.Name)
		}
	}
	return anon
}

// removeDockerVolumes removes each volume, best effort: one still in use or
// already gone stays as it is.
func removeDockerVolumes(ctx context.Context, names []string) {
	for _, n := range names {
		_ = execCommandContext(ctx, "docker", "volume", "rm", n).Run()
	}
}
