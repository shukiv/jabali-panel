package main

import (
	"fmt"
	"os"
	"path/filepath"
)

// bundledCachePluginDir is the read-only jabali-cache bundle the agent installs
// on WordPress sites from (panel-agent bundledWPCachePluginDir): cache enable
// (wordpress.cache_set) and the `jabali app refresh-cache-plugin` sweep both
// copy it into the site (JAB-64).
const bundledCachePluginDir = "/usr/local/share/jabali/wp-plugins/jabali-cache"

// syncBundledCachePlugin mirrors the repo's jabali-cache plugin (src) into the
// bundle (dst) and gives it to owner. install.sh and the release-tarball step
// do the same; without it a --from-source update left the old plugin in the
// bundle, and the refresh sweep put the old plugin back on every site. A
// checkout without the plugin is a no-op. Idempotent.
func syncBundledCachePlugin(src, dst, owner string) error {
	if !dirExists(src) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(dst), err)
	}
	// --checksum: rsync's size+mtime quick check would skip a changed file
	// that kept its size and timestamp. The plugin is small.
	if err := run("", "rsync", "-a", "--checksum", "--delete", "--exclude=.git", src+"/", dst+"/"); err != nil {
		return err
	}
	// rsync -a keeps the checkout's owner (the panel service user); the
	// bundle is root-owned so only root can change what lands on sites.
	return run("", "chown", "-R", owner, dst)
}
