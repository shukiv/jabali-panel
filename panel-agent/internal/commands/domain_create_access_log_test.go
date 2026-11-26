package commands

import (
	"strings"
	"testing"
)

// nginx inherits access_log only into a level that sets none, so a location
// with its own access_log (the page cache's status log) stops writing the
// site's access log, and everything that reads that log misses its requests.
// So every location that logs somewhere must write the site log too.
func TestVhostRender_LocationAccessLogKeepsSiteLog(t *testing.T) {
	whole := canonicalCacheVhostData()
	whole.CacheGate = ""
	for name, vd := range map[string]vhostData{"cache on a path": canonicalCacheVhostData(), "cache on the whole site": whole} {
		out := renderQAllowVhost(t, vd)
		siteLog := "access_log /var/log/nginx/" + vd.Domain + "-access.log;"
		checked := 0
		for _, b := range locationBlocks(out) {
			own := false
			for _, l := range strings.Split(b, "\n") {
				l = strings.TrimSpace(l)
				if strings.HasPrefix(l, "access_log ") && l != "access_log off;" {
					own = true
				}
			}
			if !own {
				continue
			}
			checked++
			if !strings.Contains(b, siteLog) {
				t.Errorf("%s: this location has its own access_log but does not write %q, so its requests never reach the site's access log:\n%s",
					name, siteLog, firstLine(b))
			}
		}
		if checked == 0 {
			t.Errorf("%s: no location with its own access_log was rendered; the test checks nothing", name)
		}
	}
}

// locationBlocks returns each top-level location block of an nginx config.
func locationBlocks(conf string) []string {
	var blocks []string
	lines := strings.Split(conf, "\n")
	for i := 0; i < len(lines); i++ {
		if !strings.HasPrefix(strings.TrimSpace(lines[i]), "location ") || !strings.Contains(lines[i], "{") {
			continue
		}
		var b strings.Builder
		depth, j := 0, i
		for ; j < len(lines); j++ {
			depth += strings.Count(lines[j], "{") - strings.Count(lines[j], "}")
			b.WriteString(lines[j] + "\n")
			if depth <= 0 {
				break
			}
		}
		blocks = append(blocks, b.String())
		i = j
	}
	return blocks
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
