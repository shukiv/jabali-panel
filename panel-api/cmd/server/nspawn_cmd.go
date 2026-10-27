package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// jabali nspawn — operator-only subcommands for the M13 SSH shell
// sandbox.
//
//   jabali nspawn build --codename debian-12 --version v1 --snapshot 20260426T000000Z
//   jabali nspawn list
//   jabali nspawn prune [--yes]
//
// Build is mandatory + idempotent: it refuses to overwrite an existing
// sealed image. Snapshot timestamp is required for deterministic
// rebuilds.

const (
	nspawnImagesRoot = "/var/lib/jabali-nspawn/images"
	debianSnapshot   = "https://snapshot.debian.org/archive/debian"
	defaultIncludes  = "bash,coreutils,procps,findutils,grep,sed,gawk,less,nano,ca-certificates,git,curl,wget,vim-tiny,php-cli,php-mysql,php-curl,php-xml,php-mbstring,php-zip,php-gd,unzip,rsync,mariadb-client"
	hostWpCliLink    = "/opt/wp-cli/current"
)

var (
	nspawnNameRe     = regexp.MustCompile(`^[a-z0-9-]+$`)
	nspawnSnapshotRe = regexp.MustCompile(`^\d{8}T\d{6}Z$`)
)

func newNspawnCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "nspawn",
		Short: "Manage SSH sandbox nspawn images",
	}
	cmd.AddCommand(newNspawnBuildCmd(), newNspawnListCmd(), newNspawnPruneCmd())
	return cmd
}

func newNspawnBuildCmd() *cobra.Command {
	var codename, version, snapshot, includes, suite string
	cmd := &cobra.Command{
		Use:   "build",
		Short: "debootstrap a deterministic, immutable nspawn rootfs",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !nspawnNameRe.MatchString(codename) {
				return fmt.Errorf("--codename must match [a-z0-9-]+")
			}
			if !nspawnNameRe.MatchString(version) {
				return fmt.Errorf("--version must match [a-z0-9-]+")
			}
			if !nspawnSnapshotRe.MatchString(snapshot) {
				return fmt.Errorf("--snapshot must match YYYYMMDDTHHMMSSZ (snapshot.debian.org format)")
			}
			imageName := codename + "-" + version
			imageDir := filepath.Join(nspawnImagesRoot, imageName)
			if _, err := os.Stat(imageDir); err == nil {
				return fmt.Errorf("image %s already exists at %s — refusing to overwrite", imageName, imageDir)
			}
			if err := os.MkdirAll(nspawnImagesRoot, 0o755); err != nil {
				return fmt.Errorf("mkdir %s: %w", nspawnImagesRoot, err)
			}

			// Build into a sibling .partial then atomic-rename. If
			// debootstrap dies mid-flight we leave the .partial behind
			// for inspection; nothing references the half-built image.
			partial := imageDir + ".partial"
			_ = os.RemoveAll(partial)
			if err := os.MkdirAll(partial, 0o755); err != nil {
				return fmt.Errorf("mkdir %s: %w", partial, err)
			}

			mirror := fmt.Sprintf("%s/%s/", debianSnapshot, snapshot)
			fmt.Printf("[nspawn-build] debootstrap %s --include=%s\n", suite, includes)
			fmt.Printf("[nspawn-build] mirror=%s\n", mirror)
			fmt.Printf("[nspawn-build] target=%s\n", partial)
			boot := exec.Command("debootstrap",
				"--variant=minbase",
				"--include="+includes,
				suite,
				partial,
				mirror,
			)
			boot.Stdout = os.Stdout
			boot.Stderr = os.Stderr
			if err := boot.Run(); err != nil {
				return fmt.Errorf("debootstrap failed: %w", err)
			}

			// Strip apt cache + machine-id so the image is portable.
			_ = os.WriteFile(filepath.Join(partial, "etc", "machine-id"), []byte{}, 0o644)
			cleanup := exec.Command("chroot", partial, "/bin/sh", "-c",
				"apt-get clean >/dev/null 2>&1; rm -rf /var/lib/apt/lists/* /var/cache/apt/archives/*.deb")
			cleanup.Stdout = os.Stdout
			cleanup.Stderr = os.Stderr
			_ = cleanup.Run()

			// Install wp-cli inside the rootfs by copying the host's pinned
			// phar (provisioned by install.sh under /opt/wp-cli/current).
			// Skip silently if host phar is missing — image is still usable
			// for non-WP shells; admin can rebuild after wp-cli install.
			if hostPhar, err := filepath.EvalSymlinks(hostWpCliLink); err == nil {
				dest := filepath.Join(partial, "usr/local/bin/wp")
				fmt.Printf("[nspawn-build] installing wp-cli into rootfs (from %s)\n", hostPhar)
				if cpErr := copyFileMode(hostPhar, dest, 0o755); cpErr != nil {
					return fmt.Errorf("install wp-cli into rootfs: %w", cpErr)
				}
			} else {
				fmt.Printf("[nspawn-build] WARN: host wp-cli not found at %s — image will not have wp-cli\n", hostWpCliLink)
			}

			fmt.Println("[nspawn-build] computing rootfs SHA-256")
			sum, err := hashTree(partial)
			if err != nil {
				return fmt.Errorf("hash rootfs: %w", err)
			}

			pkgList, err := capturePackageList(partial)
			if err != nil {
				return fmt.Errorf("capture package list: %w", err)
			}

			manifest := map[string]any{
				"codename":      codename,
				"version":       version,
				"snapshot":      snapshot,
				"suite":         suite,
				"mirror":        mirror,
				"includes":      includes,
				"rootfs_sha256": sum,
				"built_at":      time.Now().UTC().Format(time.RFC3339),
				"packages":      pkgList,
			}
			manifestBytes, _ := json.MarshalIndent(manifest, "", "  ")
			if err := os.WriteFile(filepath.Join(partial, "MANIFEST.json"), manifestBytes, 0o644); err != nil {
				return fmt.Errorf("write MANIFEST.json: %w", err)
			}

			// Seal: read-only after build.
			fmt.Println("[nspawn-build] sealing image read-only")
			seal := exec.Command("chmod", "-R", "a-w", partial)
			seal.Stdout = os.Stdout
			seal.Stderr = os.Stderr
			if err := seal.Run(); err != nil {
				return fmt.Errorf("seal rootfs: %w", err)
			}
			if err := os.Chmod(partial, 0o555); err != nil {
				return fmt.Errorf("chmod imageDir: %w", err)
			}

			if err := os.Rename(partial, imageDir); err != nil {
				return fmt.Errorf("rename %s -> %s: %w", partial, imageDir, err)
			}
			fmt.Printf("[nspawn-build] sealed at %s (sha256=%s)\n", imageDir, sum)
			return nil
		},
	}
	cmd.Flags().StringVar(&codename, "codename", "debian-13", "image family (e.g. debian-13)")
	cmd.Flags().StringVar(&version, "version", "", "image version label (e.g. v1, v2)")
	cmd.Flags().StringVar(&snapshot, "snapshot", "", "snapshot.debian.org timestamp YYYYMMDDTHHMMSSZ (mandatory)")
	cmd.Flags().StringVar(&includes, "includes", defaultIncludes, "comma-separated debootstrap --include list")
	cmd.Flags().StringVar(&suite, "suite", "trixie", "debootstrap suite (trixie, bookworm, ...)")
	_ = cmd.MarkFlagRequired("version")
	_ = cmd.MarkFlagRequired("snapshot")
	return cmd
}

func newNspawnListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List sealed nspawn images",
		RunE: func(cmd *cobra.Command, args []string) error {
			entries, err := os.ReadDir(nspawnImagesRoot)
			if err != nil {
				if os.IsNotExist(err) {
					fmt.Println("(no images directory yet — run jabali nspawn build first)")
					return nil
				}
				return err
			}
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				if e.IsDir() && nspawnNameRe.MatchString(e.Name()) {
					names = append(names, e.Name())
				}
			}
			sort.Strings(names)
			if len(names) == 0 {
				fmt.Println("(no sealed images)")
				return nil
			}
			fmt.Printf("%-30s %-22s %s\n", "NAME", "BUILT", "SHA256")
			for _, n := range names {
				built, sum := readManifestSummary(filepath.Join(nspawnImagesRoot, n))
				fmt.Printf("%-30s %-22s %s\n", n, built, sum)
			}
			return nil
		},
	}
}

func newNspawnPruneCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove sealed images that no user is pinned to",
		Long: `Pinned versions are read from /etc/jabali/users/<u>/nspawn-image
(reconciler-managed mirror of hosting_packages.nspawn_image_version)
plus the server-wide /etc/jabali/default-nspawn-image. Anything else
under /var/lib/jabali-nspawn/images is a candidate for removal.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			pinned := map[string]bool{}
			usersDir := "/etc/jabali/users"
			if userEntries, err := os.ReadDir(usersDir); err == nil {
				for _, ue := range userEntries {
					if !ue.IsDir() {
						continue
					}
					raw, rErr := os.ReadFile(filepath.Join(usersDir, ue.Name(), "nspawn-image"))
					if rErr != nil {
						continue
					}
					if v := strings.TrimSpace(string(raw)); v != "" {
						pinned[v] = true
					}
				}
			}
			defaultRaw, _ := os.ReadFile("/etc/jabali/default-nspawn-image")
			def := strings.TrimSpace(string(defaultRaw))
			if def != "" {
				pinned[def] = true
			}
			entries, err := os.ReadDir(nspawnImagesRoot)
			if err != nil {
				return err
			}
			candidates := []string{}
			for _, e := range entries {
				if !e.IsDir() || !nspawnNameRe.MatchString(e.Name()) {
					continue
				}
				if !pinned[e.Name()] {
					candidates = append(candidates, e.Name())
				}
			}
			if len(candidates) == 0 {
				fmt.Println("nothing to prune")
				return nil
			}
			fmt.Println("candidates for removal (no users pinned, not the default):")
			for _, c := range candidates {
				fmt.Printf("  - %s\n", c)
			}
			if !yes {
				fmt.Println("re-run with --yes to actually delete")
				return nil
			}
			for _, c := range candidates {
				p := filepath.Join(nspawnImagesRoot, c)
				if err := os.Chmod(p, 0o755); err != nil {
					return fmt.Errorf("unseal %s: %w", p, err)
				}
				if err := exec.Command("chmod", "-R", "u+w", p).Run(); err != nil {
					return fmt.Errorf("unseal -R %s: %w", p, err)
				}
				if err := os.RemoveAll(p); err != nil {
					return fmt.Errorf("rm -rf %s: %w", p, err)
				}
				fmt.Printf("removed %s\n", p)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&yes, "yes", false, "actually delete (default: dry-run)")
	// Explicit --dry-run flag accepted for muscle-memory parity.
	// Mutually exclusive with --yes; absent both = dry-run, the default.
	var dryRunDecl bool
	cmd.Flags().BoolVar(&dryRunDecl, "dry-run", false, "explicit dry-run (default; mutually exclusive with --yes)")
	cmd.MarkFlagsMutuallyExclusive("yes", "dry-run")
	return cmd
}

// hashTree walks every regular file in dir and returns a hex SHA-256 of
// the concatenated per-file digests, sorted by path. The result is
// stable regardless of disk ordering. Symlinks + special files are
// hashed by their target / type to keep the digest reflective of the
// rootfs contents.
func hashTree(dir string) (string, error) {
	type fileDigest struct {
		path string
		sum  string
	}
	var digests []fileDigest
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		info, err := d.Info()
		if err != nil {
			return err
		}
		switch {
		case info.Mode().IsRegular():
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			h := sha256.New()
			if _, err := io.Copy(h, f); err != nil {
				_ = f.Close()
				return err
			}
			_ = f.Close()
			digests = append(digests, fileDigest{rel, hex.EncodeToString(h.Sum(nil))})
		case info.Mode()&os.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			h := sha256.Sum256([]byte("symlink:" + target))
			digests = append(digests, fileDigest{rel, hex.EncodeToString(h[:])})
		case info.IsDir():
			h := sha256.Sum256([]byte("dir"))
			digests = append(digests, fileDigest{rel, hex.EncodeToString(h[:])})
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(digests, func(i, j int) bool { return digests[i].path < digests[j].path })
	final := sha256.New()
	for _, d := range digests {
		final.Write([]byte(d.path))
		final.Write([]byte{0})
		final.Write([]byte(d.sum))
		final.Write([]byte{0})
	}
	return hex.EncodeToString(final.Sum(nil)), nil
}

func capturePackageList(rootDir string) ([]map[string]string, error) {
	out, err := exec.Command("chroot", rootDir, "dpkg-query", "-W", "-f=${Package}\\t${Version}\\n").Output()
	if err != nil {
		return nil, err
	}
	var pkgs []map[string]string
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		pkgs = append(pkgs, map[string]string{"name": parts[0], "version": parts[1]})
	}
	return pkgs, nil
}

func copyFileMode(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return os.Chmod(dst, mode)
}

func readManifestSummary(dir string) (built, sum string) {
	raw, err := os.ReadFile(filepath.Join(dir, "MANIFEST.json"))
	if err != nil {
		return "—", "—"
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return "—", "—"
	}
	if v, ok := m["built_at"].(string); ok {
		built = v
	} else {
		built = "—"
	}
	if v, ok := m["rootfs_sha256"].(string); ok && len(v) >= 12 {
		sum = v[:12]
	} else {
		sum = "—"
	}
	return
}
