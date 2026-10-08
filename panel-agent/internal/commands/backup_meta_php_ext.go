package commands

import (
	"errors"
	"fmt"

	"git.jabali-panel.com/shukivaknin/jabali2/internal/backup"
	"git.jabali-panel.com/shukivaknin/jabali2/internal/phpext"
)

// enrichPHPExtensions records, for each PHP version the account's pools use,
// the extensions this server has enabled beyond the built-in ones, so a
// restore on another server can say which of them it lacks (GH #1993). A
// version it can't read is left out; the restore then can't check it.
func enrichPHPExtensions(meta *backup.AccountMetadata) error {
	installed, err := listInstalledPHPVersionsFunc()
	if err != nil {
		return err
	}
	out := map[string][]string{}
	var errs []error
	for _, pool := range meta.PHPPools {
		v := pool.PHPVersion
		if _, done := out[v]; done || !phpext.ValidVersion(v) || !containsString(installed, v) {
			continue
		}
		mods, err := readEnabledModules(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("PHP %s: %w", v, err))
			continue
		}
		exts := []string{}
		for _, s := range phpext.All() {
			if !s.BuiltIn && mods[s.EnableName] {
				exts = append(exts, s.Name)
			}
		}
		out[v] = exts
	}
	meta.PHPExtensions = out
	return errors.Join(errs...)
}
