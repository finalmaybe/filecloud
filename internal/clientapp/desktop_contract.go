package clientapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/mingming-cn/filecloud/internal/fscompat"
)

// Machine mode returns either the complete normal response or a redacted error envelope.
// Streaming sync has a separate JSONL contract; never buffer watch output.
func runMachine(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer, build BuildInfo) error {
	valid := len(args) > 0 && slices.Contains([]string{"version", "check-server", "login", "logout"}, args[0])
	if len(args) >= 2 && args[0] == "library" {
		valid = slices.Contains([]string{"list", "create", "bindings", "bind", "unbind", "status", "restore", "check-directory", "inspect"}, args[1]) || (len(args) >= 3 && args[1] == "history" && args[2] == "list")
	}
	if !valid {
		return errors.New("unsupported machine command")
	}
	var result bytes.Buffer
	if err := Run(ctx, args, stdin, &result, io.Discard, build); err != nil {
		code := syncErrorCode(err)
		return errors.Join(errors.New(code), json.NewEncoder(stdout).Encode(syncMessage{Type: "result", Outcome: "failed", Code: code}))
	}
	if len(args) >= 2 && args[0] == "library" && args[1] == "restore" {
		return json.NewEncoder(stdout).Encode(struct{}{})
	} // The durable impact is available through library status.
	_, err := result.WriteTo(stdout)
	return err
}

// Probe without credentials before the user submits an account password.
func checkServer(ctx context.Context, args []string, out, stderr io.Writer) error {
	flags := newFlagSet("check-server", stderr)
	raw := flags.String("server", "", "Filecloud server URL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	base, err := validateServerURL(*raw)
	if err != nil || flags.NArg() != 0 {
		return errors.New("invalid server address")
	}
	probe, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(probe, http.MethodGet, base.JoinPath("healthz").String(), nil)
	if err != nil {
		return err
	}
	res, err := noRedirectClient().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	var reply struct {
		RetCode *int
		Message string
	}
	if res.StatusCode != http.StatusOK || json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&reply) != nil || reply.RetCode == nil || *reply.RetCode != 0 || reply.Message != "success" {
		return errors.New("service not ready")
	}
	return json.NewEncoder(out).Encode(struct{}{})
}

func checkBindingDirectory(args []string, out, stderr io.Writer) error {
	flags := newFlagSet("library check-directory", stderr)
	path := flags.String("worktree", "", "Local worktree directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *path == "" || flags.NArg() != 0 {
		return errors.New("select a worktree directory")
	}
	root, err := openWorktreeRoot(*path, requireSupportedFilesystem)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.validateIdentity(); err != nil {
		return err
	}
	if err := fscompat.CheckDirectoryWriteAccess(root.directory); err != nil {
		return err
	}
	names, err := readDirectoryNames(root.directory, -1)
	if err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(struct {
		Empty      bool
		Filesystem string
		LocalItems int
	}{len(names) == 0, map[string]string{"linux": "ext4", "darwin": "APFS", "windows": "NTFS"}[runtime.GOOS], len(names)})
}

type localBinding struct {
	LibraryID string `json:"LibraryId"`
	DeviceID  string `json:"DeviceId"`
	Worktree  string
	Phase     string
}

func runLibraryBindings(ctx context.Context, args []string, out, stderr io.Writer) error {
	flags := newFlagSet("library bindings", stderr)
	location := flags.String("client-dir", "", "Filecloud client state directory")
	server := flags.String("server", "", "Filecloud server URL")
	owner := flags.String("user-id", "", "Account ID")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *location == "" || !validClientUUID(*owner) || flags.NArg() != 0 {
		return errors.New("usage: filecloud-client library bindings --client-dir path --server url --user-id uuid")
	}
	base, err := validateServerURL(*server)
	if err != nil {
		return err
	}
	dir, err := canonicalStateDir(*location)
	if err != nil {
		return err
	}
	records := []localBinding{}
	path := filepath.Join(dir, _clientDatabaseName)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return json.NewEncoder(out).Encode(records)
	} else if err != nil {
		return err
	}
	db, err := openClientDB(path, true)
	if err != nil {
		return err
	}
	defer db.Close()
	// A checkout can precede the first binding; after binding, the row in bindings wins.
	rows, err := db.QueryContext(ctx, `SELECT library_id,device_id,worktree,CASE MIN(priority) WHEN 0 THEN 'bound' ELSE 'binding' END FROM (
 SELECT library_id,device_id,worktree,0 AS priority FROM bindings WHERE server_url=?1 AND user_id=?2
 UNION ALL SELECT library_id,device_id,worktree,1 FROM bind_intents WHERE server_url=?1 AND user_id=?2
 UNION ALL SELECT library_id,device_id,worktree,1 FROM pending_checkouts WHERE server_url=?1 AND user_id=?2
 ) GROUP BY library_id,device_id,worktree ORDER BY library_id,worktree`, strings.TrimSuffix(strings.ToLower(base.String()), "/"), *owner)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var v localBinding
		if err := rows.Scan(&v.LibraryID, &v.DeviceID, &v.Worktree, &v.Phase); err != nil {
			return err
		}
		records = append(records, v)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	return json.NewEncoder(out).Encode(records)
}

var (
	errSyncBusy          = errors.New("binding synchronization is already running")
	errNonemptyWorktree  = errors.New("local worktree is non-empty; local import requires --import-local (issue #8)")
	errSyncBeforeRestore = errors.New("worktree is not converged with Sync Base and Head; run sync first")
	errDAVEditing        = errors.New("文档正在网络盘中编辑，本地修改已保留")
	errCannotImport      = errors.New("local and remote libraries are both non-empty; bind requires an empty worktree or empty remote library")
)

type syncStream struct {
	writer    io.Writer
	unchanged bool
}
type syncMessage struct {
	Type    string `json:"type"`
	Outcome string `json:"outcome,omitempty"`
	Code    string `json:"code,omitempty"`
}

func (s *syncStream) observe(event string) error {
	if event == "no_change" {
		s.unchanged = true
		return nil
	}
	return json.NewEncoder(s.writer).Encode(syncMessage{Type: event})
}
func (s *syncStream) finish(err error) error {
	msg := syncMessage{Type: "result", Outcome: "ok"}
	if s.unchanged {
		msg.Outcome = "no_change"
	}
	if err != nil {
		msg.Outcome = "failed"
		msg.Code = syncErrorCode(err)
		if msg.Code == "delete_confirmation" {
			msg.Outcome = "needs_confirmation"
		}
		err = errors.New(msg.Code)
	}
	return errors.Join(err, json.NewEncoder(s.writer).Encode(msg))
}

type staleSyncCandidate string

func (e staleSyncCandidate) Error() string { return string(e) }

func syncErrorCode(err error) string {
	var stale staleSyncCandidate
	var del *deleteConfirmationRequiredError
	var response *remoteStatusError
	var network net.Error
	var transient *transientRemoteError
	switch {
	case errors.Is(err, os.ErrPermission):
		return "permission_denied"
	case errors.Is(err, errSyncBeforeRestore):
		return "sync_first"
	case errors.Is(err, errDAVEditing):
		return "dav_locked"
	case errors.Is(err, _errWrongRestorePrefix):
		return "stale_candidate"
	case errors.Is(err, errUnsupportedFilesystem):
		return "unsupported_fs"
	case errors.Is(err, errNonemptyWorktree):
		return "import_required"
	case errors.Is(err, errCannotImport):
		return "no_common_base"
	case errors.As(err, &del):
		return "delete_confirmation"
	case errors.Is(err, errSyncBusy):
		return "busy"
	case errors.As(err, &stale), errors.Is(err, _errNoProtectedDeletion), errors.Is(err, _errWrongDeletePrefix):
		return "stale_candidate"
	case errors.As(err, &response) && response.status == http.StatusUnauthorized:
		return "auth_required"
	case errors.As(err, &network):
		return "offline"
	case errors.As(err, &transient):
		return "retry_later"
	case errors.Is(err, os.ErrNotExist):
		return "missing"
	default:
		return "unknown"
	}
}
