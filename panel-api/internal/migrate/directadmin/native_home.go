package directadmin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/agent"
	"git.jabali-panel.com/shukivaknin/jabali2/panel-api/internal/migrate/cpanel"
)

// NativeHomeResult sums the copies ImportNativeHome made.
type NativeHomeResult struct {
	BytesCopied   int64
	Files         int64
	DomainsCopied int
	Skipped       []string
}

// publicHTMLExcludes leaves every docroot out of the final pass, at any depth.
var publicHTMLExcludes = []string{"public_html", "public_html/", "public_html/**"}

// ImportNativeHome copies a parsed DirectAdmin backup's home (ParseNativeBackup
// lays it out as the jabali home) into /home/<destUser>/, as the cPanel split
// does: first each docroot into domains/<domain>/public_html without deleting
// anything there, then the rest of the home with every public_html left out.
// A resumed job then keeps what a site wrote since the first run, which one
// whole-home copy would delete.
func ImportNativeHome(ctx context.Context, call agent.AgentInterface, parsed *cpanel.ParsedTarball, jobID, destUser string) (*NativeHomeResult, error) {
	if call == nil || parsed == nil || parsed.HomeDir == "" || jobID == "" || destUser == "" {
		return nil, errors.New("ImportNativeHome: agent, parsed home, job and user are required")
	}
	res := &NativeHomeResult{}
	doms := make([]string, 0, len(parsed.DocRoots))
	for d := range parsed.DocRoots {
		doms = append(doms, d)
	}
	sort.Strings(doms)
	for _, dom := range doms {
		if !nativeDomainRe.MatchString(dom) {
			res.Skipped = append(res.Skipped, fmt.Sprintf("home_skip:%q: not a domain name", dom))
			continue
		}
		src := filepath.Join(parsed.HomeDir, "domains", dom, "public_html")
		if fi, err := os.Lstat(src); err != nil || !fi.IsDir() {
			res.Skipped = append(res.Skipped, fmt.Sprintf("home_skip:%s: no public_html in the backup", dom))
			continue
		}
		if err := importHome(ctx, call, res, map[string]any{
			"job_id":       jobID,
			"src_dir":      src,
			"dest_user":    destUser,
			"dest_subpath": filepath.Join("domains", dom, "public_html"),
		}); err != nil {
			return res, fmt.Errorf("%s docroot: %w", dom, err)
		}
		res.DomainsCopied++
	}
	if err := importHome(ctx, call, res, map[string]any{
		"job_id":         jobID,
		"src_dir":        parsed.HomeDir,
		"dest_user":      destUser,
		"extra_excludes": publicHTMLExcludes,
	}); err != nil {
		return res, fmt.Errorf("rest of the home: %w", err)
	}
	return res, nil
}

func importHome(ctx context.Context, call agent.AgentInterface, res *NativeHomeResult, params map[string]any) error {
	raw, err := call.Call(ctx, "migration.import_home", params)
	if err != nil {
		return err
	}
	var r struct {
		BytesCopied int64 `json:"bytes_copied"`
		Files       int64 `json:"files"`
	}
	if json.Unmarshal(raw, &r) == nil {
		res.BytesCopied += r.BytesCopied
		res.Files += r.Files
	}
	return nil
}
