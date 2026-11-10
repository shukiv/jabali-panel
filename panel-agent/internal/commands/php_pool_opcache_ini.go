package commands

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// OPcache sizing per pool (GH #2466). The FPM master allocates OPcache's
// shared memory, its file table and the JIT buffer once, at startup, from
// php.ini and the scan dirs, before it reads any pool. A pool's
// php_admin_value is applied later, in each worker, so a value there for one
// of these directives changes nothing: every master on a box ran with the
// server-wide 15-jabali-opcache.ini, whatever its pool said (PHP 8.5 refuses
// opcache.memory_consumption there outright). opcache.jit belongs here too:
// JIT can only be switched on at startup.
//
// So a pool's override for these goes into its own scan dir instead,
// /etc/php/<ver>/jabali-ext/<slug>, which fpm-exec adds after the FPM conf.d:
// the master reads it at startup and it wins over the server-wide value.
//
// Each value must match its pattern: a size (an optional K/M/G suffix, which
// PHP's own parsing accepts and an admin's raw override may carry), a count,
// or a JIT mode word or number. Nothing an ini file could read as more.
var opcacheStartupDirectives = map[string]*regexp.Regexp{
	"opcache.memory_consumption":    regexp.MustCompile(`^[0-9]{1,9}[KMGkmg]?$`),
	"opcache.max_accelerated_files": regexp.MustCompile(`^[0-9]{1,9}$`),
	"opcache.jit_buffer_size":       regexp.MustCompile(`^[0-9]{1,9}[KMGkmg]?$`),
	"opcache.jit":                   regexp.MustCompile(`^[A-Za-z0-9]{1,16}$`),
}

const phpPoolOpcacheIniName = "30-jabali-opcache.ini"

func phpPoolOpcacheIniPath(version, slug string) string {
	return filepath.Join(phpEtcRoot, version, "jabali-ext", slug, phpPoolOpcacheIniName)
}

// validOpcacheStartupValue reports whether value is well-formed for a startup
// directive. These values are written into an ini file, so only plain sizes,
// counts and JIT mode words pass.
func validOpcacheStartupValue(name, value string) bool {
	re, ok := opcacheStartupDirectives[name]
	return !ok || re.MatchString(value)
}

// splitOpcacheStartupValues separates the startup-only OPcache directives from
// the admin values the pool conf renders. It does not modify values.
func splitOpcacheStartupValues(values []KV) (pool, startup []KV) {
	for _, kv := range values {
		if _, ok := opcacheStartupDirectives[kv.Name]; ok {
			startup = append(startup, kv)
		} else {
			pool = append(pool, kv)
		}
	}
	return pool, startup
}

// applyPoolOpcacheIni writes the slug's OPcache startup ini for version, or
// removes it when there is nothing to set, and removes a copy left in another
// version's scan dir. Call it before the pool's restart. It returns whether
// anything changed: only a full restart of the master applies a change.
func applyPoolOpcacheIni(version, slug string, startup []KV) (bool, error) {
	changed := false
	others, _ := filepath.Glob(filepath.Join(phpEtcRoot, "*", "jabali-ext", slug, phpPoolOpcacheIniName))
	for _, p := range others {
		if p == phpPoolOpcacheIniPath(version, slug) {
			continue
		}
		if ok, err := removePHPDefenseFile(p); err != nil {
			return changed, fmt.Errorf("remove stale OPcache ini: %w", err)
		} else if ok {
			changed = true
		}
	}

	path := phpPoolOpcacheIniPath(version, slug)
	if len(startup) == 0 {
		ok, err := removePHPDefenseFile(path)
		if err != nil {
			return changed, fmt.Errorf("remove pool OPcache ini: %w", err)
		}
		return changed || ok, nil
	}

	var b strings.Builder
	b.WriteString("; Jabali: this pool's OPcache sizing. The master reads it at startup,\n")
	b.WriteString("; after the server-wide 15-jabali-opcache.ini (GH #2466).\n")
	b.WriteString("; RENDERED by the agent, do not edit.\n")
	for _, kv := range startup {
		b.WriteString(kv.Name + "=" + kv.Value + "\n")
	}
	wrote, err := writePHPDefenseFile(path, []byte(b.String()))
	if err != nil {
		return changed, fmt.Errorf("write pool OPcache ini: %w", err)
	}
	return changed || wrote, nil
}

// removePoolOpcacheIni deletes a slug's OPcache startup ini in every version.
// Best-effort, for pool removal and the orphan reaper.
func removePoolOpcacheIni(slug string) {
	if !phpPoolSlugRegex.MatchString(slug) || strings.Contains(slug, "..") {
		return
	}
	inis, _ := filepath.Glob(filepath.Join(phpEtcRoot, "*", "jabali-ext", slug, phpPoolOpcacheIniName))
	for _, p := range inis {
		_ = os.Remove(p)
	}
}
