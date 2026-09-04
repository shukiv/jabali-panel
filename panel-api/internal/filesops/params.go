package filesops

// Agent request params for every non-upload files.* verb.
//
// The JSON tags MUST match panel-agent/internal/commands/files_*.go exactly —
// params_test.go is the drift-guard. These types moved here verbatim from
// internal/api/files.go so both the REST and CLI adapters construct one shared
// wire contract instead of two hand-rolled ones (the CLI previously built inline
// map[string]any values that could silently diverge from these tags).
//
// admin_root is omitempty: it is set only by the REST admin File Manager (GH
// #1184); the CLI is always tenant-scoped, so it is absent on the CLI wire —
// identical to what the CLI's old maps produced (they never set the key).

type ListParams struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	AdminRoot bool   `json:"admin_root,omitempty"`
	Path      string `json:"path"`
}

type ReadParams struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	AdminRoot bool   `json:"admin_root,omitempty"`
	Path      string `json:"path"`
	// Limit is omitempty: a limit of 0 is dropped from the wire, which the agent
	// treats identically to a missing limit (files_read.go maps both to its 1 MB
	// default). The CLI download path passes 0 and so now omits the key — same
	// agent behaviour as the old explicit "limit":0.
	Limit int64 `json:"limit,omitempty"`
}

type StatParams struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	AdminRoot bool   `json:"admin_root,omitempty"`
	Path      string `json:"path"`
}

type DuParams struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	AdminRoot bool   `json:"admin_root,omitempty"`
	Path      string `json:"path"`
}

type MkdirParams struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	AdminRoot bool   `json:"admin_root,omitempty"`
	Path      string `json:"path"`
	Mode      string `json:"mode,omitempty"`
}

type WriteParams struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	AdminRoot bool   `json:"admin_root,omitempty"`
	Path      string `json:"path"`
	Content   string `json:"content"`
	Mode      string `json:"mode,omitempty"`
}

type RenameParams struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	AdminRoot bool   `json:"admin_root,omitempty"`
	OldPath   string `json:"old_path"`
	NewPath   string `json:"new_path"`
}

type MoveParams struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	AdminRoot bool   `json:"admin_root,omitempty"`
	OldPath   string `json:"old_path"`
	NewPath   string `json:"new_path"`
}

type CopyParams struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	AdminRoot bool   `json:"admin_root,omitempty"`
	SrcPath   string `json:"src_path"`
	DstPath   string `json:"dst_path"`
}

type ChmodParams struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	AdminRoot bool   `json:"admin_root,omitempty"`
	Path      string `json:"path"`
	Mode      string `json:"mode"`
}

type DeleteParams struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	AdminRoot bool   `json:"admin_root,omitempty"`
	Path      string `json:"path"`
	Recursive bool   `json:"recursive,omitempty"`
}

type ArchiveParams struct {
	UserID    string   `json:"user_id"`
	Username  string   `json:"username"`
	AdminRoot bool     `json:"admin_root,omitempty"`
	Paths     []string `json:"paths"`
}

type ExtractParams struct {
	UserID    string `json:"user_id"`
	Username  string `json:"username"`
	AdminRoot bool   `json:"admin_root,omitempty"`
	Path      string `json:"path"`
	Dest      string `json:"dest,omitempty"`
}

// ---- builders: stamp a Scope onto the verb-specific fields ----
//
// Each returns the concrete params struct for its verb; the adapter passes it to
// its agent client with the matching Method* constant. rename/move/copy take an
// already-derived target path (from RenameTarget / MoveTarget / CopyTarget) so
// the REST admin-scope pre-check and the agent call share one derivation.

func List(s Scope, path string) ListParams {
	return ListParams{UserID: s.UserID, Username: s.Username, AdminRoot: s.AdminRoot, Path: path}
}

func Read(s Scope, path string, limit int64) ReadParams {
	return ReadParams{UserID: s.UserID, Username: s.Username, AdminRoot: s.AdminRoot, Path: path, Limit: limit}
}

func Stat(s Scope, path string) StatParams {
	return StatParams{UserID: s.UserID, Username: s.Username, AdminRoot: s.AdminRoot, Path: path}
}

func Du(s Scope, path string) DuParams {
	return DuParams{UserID: s.UserID, Username: s.Username, AdminRoot: s.AdminRoot, Path: path}
}

func Mkdir(s Scope, path string) MkdirParams {
	return MkdirParams{UserID: s.UserID, Username: s.Username, AdminRoot: s.AdminRoot, Path: path}
}

func Write(s Scope, path, content string) WriteParams {
	return WriteParams{UserID: s.UserID, Username: s.Username, AdminRoot: s.AdminRoot, Path: path, Content: content}
}

func Rename(s Scope, oldPath, newPath string) RenameParams {
	return RenameParams{UserID: s.UserID, Username: s.Username, AdminRoot: s.AdminRoot, OldPath: oldPath, NewPath: newPath}
}

func Move(s Scope, oldPath, newPath string) MoveParams {
	return MoveParams{UserID: s.UserID, Username: s.Username, AdminRoot: s.AdminRoot, OldPath: oldPath, NewPath: newPath}
}

func Copy(s Scope, srcPath, dstPath string) CopyParams {
	return CopyParams{UserID: s.UserID, Username: s.Username, AdminRoot: s.AdminRoot, SrcPath: srcPath, DstPath: dstPath}
}

func Chmod(s Scope, path, mode string) ChmodParams {
	return ChmodParams{UserID: s.UserID, Username: s.Username, AdminRoot: s.AdminRoot, Path: path, Mode: mode}
}

func Delete(s Scope, path string, recursive bool) DeleteParams {
	return DeleteParams{UserID: s.UserID, Username: s.Username, AdminRoot: s.AdminRoot, Path: path, Recursive: recursive}
}

func Archive(s Scope, paths []string) ArchiveParams {
	return ArchiveParams{UserID: s.UserID, Username: s.Username, AdminRoot: s.AdminRoot, Paths: paths}
}

func Extract(s Scope, path, dest string) ExtractParams {
	return ExtractParams{UserID: s.UserID, Username: s.Username, AdminRoot: s.AdminRoot, Path: path, Dest: dest}
}
