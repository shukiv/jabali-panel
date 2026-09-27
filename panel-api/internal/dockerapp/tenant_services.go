package dockerapp

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/tenantcompose"
)

// RenderTenant renders a tenant install's compose and the service set the
// agent must find in it (GH #1903).
//
// The service set comes from a second render of the same template with every
// tenant-supplied value removed: no env values, no domain, no ports, no name.
// What remains (slug, data root, catalog image, limits, hardening) is computed
// by panel-api. So if a tenant value ever broke out of its YAML scalar and
// added a service, a network or a limit, the real render would carry it and
// this set would not, and the agent refuses the install.
func RenderTenant(entry Entry, params RenderParams) (string, tenantcompose.Services, error) {
	if params.TenantHardening == nil {
		return "", nil, errors.New("a tenant render needs the tenant hardening profile")
	}
	compose, err := Render(entry, params)
	if err != nil {
		return "", nil, err
	}
	skeleton := params
	skeleton.Name = ""
	skeleton.Domain = ""
	skeleton.Ports = nil
	skeleton.Env = nil
	skeletonYML, err := Render(entry, skeleton)
	if err != nil {
		return "", nil, fmt.Errorf("render the expected service set for %q: %w", entry.Slug, err)
	}
	services, err := ServicesFromCompose(skeletonYML)
	if err != nil {
		return "", nil, fmt.Errorf("expected service set for %q: %w", entry.Slug, err)
	}
	return compose, services, nil
}

// ServicesFromCompose reads the pinned fields of every service in a rendered
// compose: image, deploy.resources.limits (cpus, memory, pids), shm_size and
// ulimits. Sizes are converted to bytes the way docker compose converts them,
// so the agent can compare against the resolved `docker compose config`.
func ServicesFromCompose(composeYML string) (tenantcompose.Services, error) {
	var doc struct {
		Services map[string]map[string]any `yaml:"services"`
	}
	if err := yaml.Unmarshal([]byte(composeYML), &doc); err != nil {
		return nil, fmt.Errorf("parse compose: %w", err)
	}
	if len(doc.Services) == 0 {
		return nil, errors.New("compose has no services")
	}
	out := make(tenantcompose.Services, len(doc.Services))
	for name, svc := range doc.Services {
		s, err := serviceFromCompose(svc)
		if err != nil {
			return nil, fmt.Errorf("service %q: %w", name, err)
		}
		out[name] = s
	}
	return out, nil
}

func serviceFromCompose(svc map[string]any) (tenantcompose.Service, error) {
	var s tenantcompose.Service
	img, ok := svc["image"].(string)
	if !ok || strings.TrimSpace(img) == "" {
		return s, errors.New("has no image")
	}
	s.Image = img
	var err error
	if limits := nestedMap(svc, "deploy", "resources", "limits"); limits != nil {
		if s.CPUs, err = composeFloat(limits["cpus"]); err != nil {
			return s, fmt.Errorf("cpus: %w", err)
		}
		if s.MemoryBytes, err = composeBytes(limits["memory"]); err != nil {
			return s, fmt.Errorf("memory: %w", err)
		}
		if s.PIDs, err = composeInt(limits["pids"]); err != nil {
			return s, fmt.Errorf("pids: %w", err)
		}
	}
	if s.ShmSizeBytes, err = composeBytes(svc["shm_size"]); err != nil {
		return s, fmt.Errorf("shm_size: %w", err)
	}
	if raw, ok := svc["ulimits"].(map[string]any); ok && len(raw) > 0 {
		s.Ulimits = make(map[string]tenantcompose.Ulimit, len(raw))
		for n, v := range raw {
			u, err := composeUlimit(v)
			if err != nil {
				return s, fmt.Errorf("ulimit %s: %w", n, err)
			}
			s.Ulimits[n] = u
		}
	}
	return s, nil
}

func nestedMap(m map[string]any, keys ...string) map[string]any {
	for _, k := range keys {
		next, ok := m[k].(map[string]any)
		if !ok {
			return nil
		}
		m = next
	}
	return m
}

// composeFloat reads a cpus value: a quoted string ("0.5") or a number.
// Absent or empty means no limit.
func composeFloat(v any) (float64, error) {
	switch t := v.(type) {
	case nil:
		return 0, nil
	case int:
		return float64(t), nil
	case float64:
		return t, nil
	case string:
		if strings.TrimSpace(t) == "" {
			return 0, nil
		}
		return strconv.ParseFloat(strings.TrimSpace(t), 64)
	}
	return 0, fmt.Errorf("unexpected value %v", v)
}

// composeBytes reads a byte size: a unit string ("512m") or a plain number of
// bytes. Absent or empty means not set.
func composeBytes(v any) (int64, error) {
	switch t := v.(type) {
	case nil:
		return 0, nil
	case int:
		return int64(t), nil
	case string:
		if strings.TrimSpace(t) == "" {
			return 0, nil
		}
		return tenantcompose.RAMInBytes(t)
	}
	return 0, fmt.Errorf("unexpected value %v", v)
}

func composeInt(v any) (int64, error) {
	switch t := v.(type) {
	case nil:
		return 0, nil
	case int:
		return int64(t), nil
	case string:
		return strconv.ParseInt(strings.TrimSpace(t), 10, 64)
	}
	return 0, fmt.Errorf("unexpected value %v", v)
}

// composeUlimit reads one ulimit: a single number (soft = hard) or a
// {soft, hard} mapping.
func composeUlimit(v any) (tenantcompose.Ulimit, error) {
	if m, ok := v.(map[string]any); ok {
		soft, err := composeInt(m["soft"])
		if err != nil {
			return tenantcompose.Ulimit{}, err
		}
		hard, err := composeInt(m["hard"])
		if err != nil {
			return tenantcompose.Ulimit{}, err
		}
		return tenantcompose.Ulimit{Soft: soft, Hard: hard}, nil
	}
	n, err := composeInt(v)
	if err != nil {
		return tenantcompose.Ulimit{}, err
	}
	return tenantcompose.Ulimit{Soft: n, Hard: n}, nil
}
