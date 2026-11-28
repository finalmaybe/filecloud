package clientapp

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mingming-cn/filecloud/internal/fscompat"
)

const checkoutFileReplacementSchema = `CREATE TABLE checkout_file_replacements (
 worktree TEXT PRIMARY KEY NOT NULL REFERENCES pending_checkouts(worktree) ON DELETE CASCADE,
 path TEXT NOT NULL)`

func migrateCheckoutFileReplacementSchema(ctx context.Context, tx *sql.Tx) error {
	if _, err := tx.ExecContext(ctx, strings.Replace(checkoutFileReplacementSchema, "CREATE TABLE", "CREATE TABLE IF NOT EXISTS", 1)); err != nil {
		return err
	}
	return validateCheckoutFileReplacementSchema(ctx, tx)
}

func validateCheckoutFileReplacementSchema(ctx context.Context, db clientSchemaQuerier) error {
	var schema string
	if err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type='table' AND name='checkout_file_replacements'`).Scan(&schema); err != nil || canonicalSQLiteSQL(schema) != canonicalSQLiteSQL(checkoutFileReplacementSchema) {
		return errors.Join(errors.New("invalid single-file checkout schema"), err)
	}
	var triggers int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE type='trigger' AND tbl_name='checkout_file_replacements'`).Scan(&triggers); err != nil || triggers != 0 {
		return errors.Join(errors.New("invalid single-file checkout triggers"), err)
	}
	return nil
}

func loadCheckoutFileReplacement(ctx context.Context, db *sql.DB, worktree string) (string, error) {
	var path string
	err := db.QueryRowContext(ctx, `SELECT path FROM checkout_file_replacements WHERE worktree=?`, worktree).Scan(&path)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil || !_validConflictPromotionPath(path) {
		return "", errors.Join(errors.New("invalid single-file checkout path"), err)
	}
	return path, nil
}

var errFileReplacementSourceChanged = errors.New("local file changed before remote replacement")

// A capture can be durable before sync_recoveries.completed is set. The
// caller must also check that the hidden name is absent in the pinned parent.
func fileReplacementCaptureUnstarted(ctx context.Context, db *sql.DB, value syncRecovery, parent string) (bool, error) {
	if value.completed || value.tombstone != "" {
		return false, nil
	}
	var registered bool
	err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM fs_actions
		WHERE worktree=? AND phase='pre_base' AND op='rename' AND parent_path=? AND target_name=?)`,
		value.worktree, parent, value.name).Scan(&registered)
	return !registered && err == nil, err
}

// Cancel a capture intent when the recovery name is absent and a changed
// regular file is visible. In particular, an atomic editor save may have
// replaced the original inode; the pending rename must not adopt it.
func cancelChangedFileReplacementCapture(ctx context.Context, db *sql.DB, root *openedWorktree, action fsAction) (bool, error) {
	if action.Phase != fsPhasePreBase || action.Op != fsOpRename || action.ExpectedKind != "File" ||
		!strings.HasPrefix(action.InternalTarget, syncRecoveryPrefix) || action.InternalTarget != action.Target ||
		action.InternalSource != "" || action.OriginActionID != "" || action.ExpectedObject != "" {
		return false, nil
	}
	file, err := loadCheckoutFileReplacement(ctx, db, action.Worktree)
	if err != nil || file == "" || file != joinScanPath(action.Parent, action.Source) {
		return false, err
	}
	values, err := loadSyncRecoveries(ctx, db, action.Worktree)
	if err != nil {
		return false, err
	}
	if len(values) != 1 || values[0].path != file || values[0].kind != "File" || values[0].name != action.Target ||
		values[0].device != action.ExpectedDevice || values[0].inode != action.ExpectedInode || values[0].completed || values[0].tombstone != "" {
		return false, errors.New("single-file capture intent does not match its registered recovery")
	}
	parent, err := openFSActionParent(root, action.Parent, action.ParentDevice, action.ParentInode)
	if err != nil {
		return false, err
	}
	defer parent.Close()
	visibleWithoutRecovery := func() (fscompat.Stat_t, bool, error) {
		source, err := statAt(parent, action.Source)
		if errors.Is(err, fscompat.ENOENT) {
			return source, false, nil
		}
		if err != nil || !statMatchesCheckoutTarget(source, "File", action.ParentDevice, source.Ino) {
			return source, false, err
		}
		_, err = statAt(parent, action.Target)
		if errors.Is(err, fscompat.ENOENT) {
			return source, true, nil
		}
		return source, false, err
	}
	source, visible, err := visibleWithoutRecovery()
	if err != nil || !visible {
		return false, err
	}
	if statMatchesRecovery(source, values[0]) && verifyNamedRecovery(parent, action.Source, values[0]) == nil {
		return false, nil
	}
	current, visible, err := visibleWithoutRecovery()
	if err != nil || !visible || current.Dev != source.Dev || current.Ino != source.Ino {
		return false, err
	}
	result, err := db.ExecContext(ctx, `DELETE FROM fs_actions WHERE worktree=? AND action_id=? AND state='intent'
		AND EXISTS (SELECT 1 FROM pending_checkouts WHERE worktree=? AND apply_state IN ('applying','rolling_back'))`,
		action.Worktree, action.ActionID, action.Worktree)
	if err != nil {
		return false, err
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return false, errors.Join(errors.New("could not cancel unexecuted file replacement capture"), err)
	}
	return true, nil
}

type syncFileReplacementPlan struct {
	old     checkoutPath
	targets []checkoutPath
	dirs    map[string]retainedCheckoutDirectory
}

// A directory object ID includes its children. It can change without a directory
// replacement; compare leaf entries and require an identical path/type layout.
func planSyncFileReplacement(ctx context.Context, db *sql.DB, options bindOptions, baseRoot string,
	stable worktreeSnapshot, targets, omitted []checkoutPath) (*syncFileReplacementPlan, error) {
	if stable.needsCheckout || len(stable.paths) != len(targets) {
		return nil, nil
	}
	var hasIntents bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM checkout_paths WHERE worktree=?)`, options.worktree).Scan(&hasIntents); err != nil || hasIntents {
		return nil, err // An older, already-persisted plan keeps its original scope.
	}
	baseline := stable.paths
	if options.scanConfig.ignore.Active() {
		// Protected directory mtimes and omitted entries in the filesystem view
		// need not match the canonical Sync Base. Compare with its verified view.
		var priorOmitted []checkoutPath
		var err error
		baseline, priorOmitted, err = deriveRemotePathsWithRules(ctx, options, baseRoot, false, options.scanConfig.ignore)
		if err != nil {
			return nil, err
		}
		if len(priorOmitted) != len(omitted) {
			return nil, nil
		}
		for i, prior := range priorOmitted {
			if prior != omitted[i] {
				return nil, nil
			}
		}
	}
	if len(baseline) != len(targets) {
		return nil, nil
	}
	base := make(map[string]checkoutPath, len(baseline))
	for _, path := range baseline {
		base[path.path] = path
	}
	var changed string
	for _, target := range targets {
		prior, ok := base[target.path]
		if !ok || prior.kind != target.kind {
			return nil, nil
		}
		if target.kind == "File" && !sameCheckoutFile(prior, target) {
			if changed != "" {
				return nil, nil
			}
			changed = target.path
		}
	}
	if changed == "" {
		return nil, nil
	}
	for _, target := range targets {
		if target.kind == "Directory" && base[target.path].mtime != target.mtime && !isCheckoutAncestor(target.path, changed) {
			return nil, nil
		}
	}
	plan := &syncFileReplacementPlan{targets: fileReplacementTargets(targets, changed), dirs: make(map[string]retainedCheckoutDirectory)}
	for _, path := range stable.paths {
		switch {
		case path.path == changed:
			if path.kind != "File" || !sameCheckoutFile(path, base[changed]) {
				return nil, nil
			}
			plan.old = path
		case isCheckoutAncestor(path.path, changed):
			if path.kind != "Directory" {
				return nil, nil
			}
			if kept, ok := options.retainedDirs[path.path]; ok && (kept.device != path.device || kept.inode != path.inode) {
				return nil, errSyncIgnoreBlocked
			}
			dir, err := openFSActionParent(options.worktreeRoot, path.path, path.device, path.inode)
			if err != nil {
				return nil, err
			}
			stat, err := statOfOpenFile(dir)
			if err := errors.Join(err, dir.Close()); err != nil {
				return nil, err
			}
			if canonicalStatMtime(stat) != path.mtime {
				return nil, fmt.Errorf("checkout ancestor %q changed during recovery planning", path.path)
			}
			plan.dirs[path.path] = retainedCheckoutDirectory{path: path.path, device: path.device, inode: path.inode,
				mtime: time.Unix(stat.Mtim.Sec, stat.Mtim.Nsec).UTC().Format(time.RFC3339Nano)}
		}
	}
	if plan.old.path == "" || len(plan.dirs)+1 != len(plan.targets) || len(plan.targets) != strings.Count(changed, "/")+1 {
		return nil, nil
	}
	return plan, nil
}

func sameCheckoutFile(a, b checkoutPath) bool {
	return a.id == b.id && a.mtime == b.mtime && a.size == b.size
}

func isCheckoutAncestor(dir, file string) bool {
	return strings.HasPrefix(file, dir+"/")
}

func fileReplacementTargets(paths []checkoutPath, file string) []checkoutPath {
	var targets []checkoutPath
	for _, path := range paths {
		if path.path == file && path.kind == "File" || path.kind == "Directory" && isCheckoutAncestor(path.path, file) {
			targets = append(targets, path)
		}
	}
	return targets
}

func saveSyncFileReplacement(ctx context.Context, tx *sql.Tx, worktree string, plan *syncFileReplacementPlan) error {
	if _, err := tx.ExecContext(ctx, `INSERT INTO checkout_file_replacements(worktree,path) VALUES (?,?)`, worktree, plan.old.path); err != nil {
		return err
	}
	// The retained-directory journal also supplies the identity for nested
	// recovery names when no user ignore rules are active.
	if _, err := tx.ExecContext(ctx, `INSERT INTO checkout_ignore_plans(worktree,rules_json) VALUES (?,'{}') ON CONFLICT(worktree) DO NOTHING`, worktree); err != nil {
		return err
	}
	for _, dir := range plan.dirs {
		result, err := tx.ExecContext(ctx, `INSERT INTO checkout_retained_directories(worktree,path,device,inode,mtime) VALUES (?,?,?,?,?)
			ON CONFLICT(worktree,path) DO UPDATE SET mtime=excluded.mtime WHERE device=excluded.device AND inode=excluded.inode`,
			worktree, dir.path, dir.device, dir.inode, dir.mtime)
		if err != nil {
			return err
		}
		if changed, err := result.RowsAffected(); err != nil || changed != 1 {
			return errors.Join(errors.New("single-file checkout ancestor identity changed"), err)
		}
	}
	return saveCheckoutPathsWith(ctx, tx, worktree, plan.targets)
}

func validateFileReplacementIntents(ctx context.Context, db *sql.DB, options bindOptions, targets []checkoutPath, promotions int) error {
	file := options.syncFileReplacement
	if promotions != 0 || len(targets) != strings.Count(file, "/")+1 || targets[len(targets)-1].path != file {
		return errors.New("invalid single-file checkout scope")
	}
	var count, matching int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*), COUNT(CASE WHEN path=? AND type='File' THEN 1 END)
		FROM sync_recoveries WHERE worktree=?`, file, options.worktree).Scan(&count, &matching); err != nil || count != 1 || matching != 1 {
		return errors.Join(errors.New("single-file checkout recovery does not match its scope"), err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM checkout_paths WHERE worktree=?`, options.worktree).Scan(&count); err != nil || count != len(targets) {
		return errors.Join(errors.New("single-file checkout intents do not match their scope"), err)
	}
	for _, path := range targets {
		if path.kind == "Directory" {
			if _, ok := options.retainedDirs[path.path]; !ok {
				return fmt.Errorf("single-file checkout ancestor %q has no retained identity", path.path)
			}
		}
		if err := db.QueryRowContext(ctx, `SELECT 1 FROM checkout_paths WHERE worktree=? AND path=? AND type=?
			AND object_id=? AND canonical_mtime=? AND size=?`, options.worktree, path.path, path.kind, path.id, path.mtime, path.size).Scan(&matching); err != nil {
			return fmt.Errorf("single-file checkout intent %q does not match its target: %w", path.path, err)
		}
	}
	return nil
}

// Assemble and verify the new file before moving the old file off its path.
// After an interrupted install, the existing installer verifies the visible
// inode against its durable temporary-file identity instead of staging again.
func stageSyncFileReplacement(ctx context.Context, db *sql.DB, options bindOptions, target checkoutPath, config libraryClientConfig) (*stagedCheckoutFile, error) {
	parent, name, err := openCheckoutParent(options.worktreeRoot, target.path, checkoutParentVerifier(ctx, db, options.worktree))
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	var captured bool
	if err := db.QueryRowContext(ctx, `SELECT completed FROM sync_recoveries WHERE worktree=? AND path=?`, options.worktree, target.path).Scan(&captured); err != nil {
		return nil, err
	}
	if captured {
		if _, err := statAt(parent, name); err == nil {
			return nil, nil
		} else if !errors.Is(err, fscompat.ENOENT) {
			return nil, err
		}
	}
	return stageCheckoutFileAt(ctx, db, options, target, parent, config)
}

func verifyStagedFileReplacement(options bindOptions, target checkoutPath, parent *os.File, staged *stagedCheckoutFile) error {
	before, err := statAt(parent, staged.name)
	if err != nil {
		return err
	}
	expected := rollbackCheckoutPath{path: target.path, kind: "File", id: target.id, mtime: target.mtime, size: target.size}
	if err := verifyRollbackCheckoutTarget(parent, options.cacheRoot, staged.name, expected, staged.device, staged.inode); err != nil {
		return fmt.Errorf("verify staged checkout file %q: %w", target.path, err)
	}
	after, err := statAt(parent, staged.name)
	if err != nil || !sameRollbackFileState(before, after) {
		return errors.Join(errors.New("staged checkout file changed during verification"), err)
	}
	staged.verified = after
	return nil
}

func verifyFileReplacementIdentities(snapshot worktreeSnapshot, dirs map[string]retainedCheckoutDirectory, installed checkoutPath) error {
	found := false
	for _, path := range snapshot.paths {
		if path.path == installed.path {
			if path.device != installed.device || path.inode != installed.inode {
				return fmt.Errorf("installed checkout file %q changed identity", path.path)
			}
			found = true
		} else if isCheckoutAncestor(path.path, installed.path) {
			dir := dirs[path.path]
			if path.device != dir.device || path.inode != dir.inode {
				return fmt.Errorf("single-file checkout ancestor %q changed identity", path.path)
			}
		}
	}
	if !found {
		return fmt.Errorf("installed checkout file %q is missing", installed.path)
	}
	return nil
}

func setFileReplacementDirectoryMtime(ctx context.Context, db *sql.DB, root *openedWorktree, worktree, phase string,
	target checkoutPath, dir retainedCheckoutDirectory, fault fsActionFault) error {
	opened, err := openFSActionParent(root, target.path, dir.device, dir.inode)
	if err != nil {
		return err
	}
	stat, err := statOfOpenFile(opened)
	if err := errors.Join(err, opened.Close()); err != nil {
		return err
	}
	actual := canonicalStatMtime(stat)
	if phase == fsPhaseRollback {
		actual = time.Unix(stat.Mtim.Sec, stat.Mtim.Nsec).UTC().Format(time.RFC3339Nano)
	}
	if actual == target.mtime {
		return nil
	}
	parent, leaf := splitFSActionPath(target.path)
	return journalMtime(ctx, db, root, worktree, phase, parent, leaf, "Directory", target.mtime, dir.device, dir.inode, fault)
}

func setFileReplacementParentAfterCleanup(ctx context.Context, db *sql.DB, root *openedWorktree, worktree, parent string,
	fault fsActionFault) error {
	if parent == "" { // The worktree root mtime is not part of the Snapshot.
		return nil
	}
	var dir retainedCheckoutDirectory
	target := checkoutPath{path: parent}
	if err := db.QueryRowContext(ctx, `SELECT p.canonical_mtime,d.device,d.inode FROM checkout_paths p
		JOIN checkout_retained_directories d USING(worktree,path)
		WHERE p.worktree=? AND p.path=? AND p.type='Directory' AND p.completed=1`, worktree, parent).
		Scan(&target.mtime, &dir.device, &dir.inode); err != nil {
		return err
	}
	return setFileReplacementDirectoryMtime(ctx, db, root, worktree, fsPhasePostBase, target, dir, fault)
}

// A finalized replacement no longer owns anything in a moved parent once
// the exact recovery tombstone's unlink has been recorded as Completed.
func removedFileReplacementParent(ctx context.Context, db *sql.DB, worktree, parent string) (retainedCheckoutDirectory, string, error) {
	var dir retainedCheckoutDirectory
	var file, mtime string
	err := db.QueryRowContext(ctx, `SELECT r.path,d.device,d.inode,p.canonical_mtime
		FROM checkout_file_replacements f
		JOIN pending_checkouts c USING(worktree)
		JOIN sync_recoveries r USING(worktree,path)
		JOIN checkout_retained_directories d ON d.worktree=f.worktree AND d.path=?
		JOIN checkout_paths p ON p.worktree=d.worktree AND p.path=d.path AND p.type='Directory' AND p.completed=1
		JOIN fs_actions a ON a.worktree=r.worktree AND a.phase='post_base' AND a.op='unlink' AND a.state='completed'
			AND a.action_outcome='' AND a.parent_path=d.path AND a.parent_device=d.device AND a.parent_inode=d.inode
			AND a.source_name=r.tombstone_name AND a.internal_source=r.tombstone_name
			AND a.expected_kind='File' AND a.expected_device=r.device AND a.expected_inode=r.inode
			AND a.expected_object=r.object_id AND a.expected_size=r.size AND a.expected_mtime=r.canonical_mtime
		WHERE f.worktree=? AND c.apply_state='finalized' AND r.type='File' AND r.completed=1 AND r.tombstone_name<>''`,
		parent, worktree).Scan(&file, &dir.device, &dir.inode, &mtime)
	if errors.Is(err, sql.ErrNoRows) {
		return retainedCheckoutDirectory{}, "", nil
	}
	if err != nil {
		return retainedCheckoutDirectory{}, "", err
	}
	if actualParent, _ := splitFSActionPath(file); actualParent != parent || dir.device == 0 || dir.inode == 0 {
		return retainedCheckoutDirectory{}, "", errors.New("invalid completed file replacement removal evidence")
	}
	dir.path = parent
	return dir, mtime, nil
}

func fileReplacementParentMoved(root *openedWorktree, dir retainedCheckoutDirectory) (bool, error) {
	current, err := openFSActionParent(root, dir.path, 0, 0)
	if errors.Is(err, fscompat.ENOENT) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	stat, err := statOfOpenFile(current)
	if err := errors.Join(err, current.Close()); err != nil {
		return false, err
	}
	return !statMatchesCheckoutTarget(stat, "Directory", dir.device, dir.inode), nil
}

func completeObsoleteFileReplacementMtime(ctx context.Context, db *sql.DB, root *openedWorktree, action fsAction) (bool, error) {
	if action.Phase != fsPhasePostBase || action.Op != fsOpMtime || action.ExpectedKind != "Directory" || action.Source == "" {
		return false, nil
	}
	dir, mtime, err := removedFileReplacementParent(ctx, db, action.Worktree, joinScanPath(action.Parent, action.Source))
	if err != nil || dir.path == "" || dir.device != action.ExpectedDevice || dir.inode != action.ExpectedInode || mtime != action.ExpectedMtime {
		return false, err
	}
	moved, err := fileReplacementParentMoved(root, dir)
	if err != nil || !moved {
		return false, err
	}
	result, err := db.ExecContext(ctx, `UPDATE fs_actions SET state='completed' WHERE worktree=? AND action_id=? AND state='intent'`, action.Worktree, action.ActionID)
	if err != nil {
		return false, err
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return false, errors.Join(errors.New("could not complete obsolete file replacement mtime"), err)
	}
	return true, nil
}

func openFileReplacementTemp(parent *os.File, name string, device, inode uint64) (int, fscompat.Stat_t, error) {
	var stat fscompat.Stat_t
	entry, err := statAt(parent, name)
	if err != nil || !statMatchesCheckoutTarget(entry, "File", device, inode) {
		return -1, stat, errors.Join(errors.New("registered checkout temporary file identity changed"), err)
	}
	fd, err := fscompat.Openat(int(parent.Fd()), name, fscompat.O_RDWR|fscompat.O_NOFOLLOW|fscompat.O_CLOEXEC, 0)
	if err != nil {
		return -1, stat, err
	}
	if err := fscompat.Fstat(fd, &stat); err != nil || !statMatchesCheckoutTarget(stat, "File", device, inode) {
		return -1, stat, errors.Join(errors.New("registered checkout temporary file identity changed while opening"), err, fscompat.Close(fd))
	}
	return fd, stat, nil
}

// fileReplacementInstall returns journal evidence that this exact captured file
// was replaced at the pinned pathname, not just that a temp was registered.
func fileReplacementInstall(ctx context.Context, db *sql.DB, worktree, path string, journal []fsAction) (fsAction, syncRecovery, error) {
	temp, device, inode, _, err := checkoutTempRecord(ctx, db, worktree, path)
	if err != nil || temp == "" || device == 0 || inode == 0 {
		return fsAction{}, syncRecovery{}, err
	}
	parent, name := splitFSActionPath(path)
	var installed fsAction
	for _, action := range journal {
		if action.Phase == fsPhasePreBase && action.Op == fsOpRename && action.State == fsStateCompleted &&
			action.Parent == parent && action.Source == temp && action.InternalSource == temp && action.Target == name && action.InternalTarget == "" &&
			action.ExpectedKind == "File" && action.ExpectedDevice == device && action.ExpectedInode == inode && action.ExpectedObject == "" {
			if installed.ActionID != "" {
				return fsAction{}, syncRecovery{}, errors.New("multiple durable single-file installations")
			}
			installed = action
		}
	}
	if installed.ActionID == "" {
		return fsAction{}, syncRecovery{}, nil
	}
	values, err := loadSyncRecoveries(ctx, db, worktree)
	if err != nil {
		return fsAction{}, syncRecovery{}, err
	}
	if len(values) != 1 || values[0].path != path || values[0].kind != "File" || !values[0].completed || values[0].tombstone != "" {
		return fsAction{}, syncRecovery{}, errors.New("durable single-file installation has no exact captured recovery")
	}
	recovery := values[0]
	for _, action := range journal {
		if action.Order < installed.Order && action.Phase == fsPhasePreBase && action.Op == fsOpRename && action.State == fsStateCompleted &&
			action.Parent == parent && action.ParentDevice == installed.ParentDevice && action.ParentInode == installed.ParentInode &&
			action.Source == name && action.InternalSource == "" && action.Target == recovery.name && action.InternalTarget == recovery.name &&
			action.ExpectedKind == "File" && action.ExpectedDevice == recovery.device && action.ExpectedInode == recovery.inode && action.ExpectedObject == "" {
			return installed, recovery, nil
		}
	}
	return fsAction{}, syncRecovery{}, errors.New("durable single-file installation has no completed capture action")
}

func sameRollbackFileReplacementParent(action, install fsAction) bool {
	return action.Order > install.Order && action.Phase == fsPhaseRollback && action.ExpectedKind == "File" &&
		action.Parent == install.Parent && action.ParentDevice == install.ParentDevice && action.ParentInode == install.ParentInode
}

func isFileReplacementSaveAction(action fsAction) bool {
	return action.Phase == fsPhaseRollback && action.Op == fsOpRename && action.ExpectedKind == "File" &&
		action.InternalSource == "" && action.InternalTarget == "" && action.ExpectedObject == "" && action.OriginActionID == ""
}

// rollbackFileReplacementSave handles only a local save or deletion after a
// durable installation. Unmodified installs still use the existing rollback.
func rollbackFileReplacementSave(ctx context.Context, db *sql.DB, root *openedWorktree, parent, cacheRoot *os.File,
	worktree, name string, value rollbackCheckoutPath, fault fsActionFault) (bool, error) {
	if value.rollbackName != "" {
		return false, nil
	}
	journal, err := loadFSActions(ctx, db, worktree)
	if err != nil {
		return false, err
	}
	installed, recovery, err := fileReplacementInstall(ctx, db, worktree, value.path, journal)
	if err != nil || installed.ActionID == "" {
		return false, err
	}
	parentStat, err := statOfOpenFile(parent)
	if err != nil || uint64(parentStat.Dev) != installed.ParentDevice || parentStat.Ino != installed.ParentInode {
		return false, errors.Join(errors.New("installed single-file parent changed identity"), err)
	}
	for _, action := range journal {
		if sameRollbackFileReplacementParent(action, installed) && action.State == fsStateCompleted &&
			action.Op == fsOpUnlink && action.Source == recovery.name && action.InternalSource == recovery.name &&
			action.ExpectedDevice == recovery.device && action.ExpectedInode == recovery.inode && action.ExpectedObject == recovery.id &&
			action.ExpectedMtime == recovery.mtime && action.ExpectedSize == recovery.size {
			return true, consumeSyncRecovery(ctx, db, recovery)
		}
	}
	// Only fall through to the normal (destructive) rollback after checking
	// for a completed recovery unlink. A user can move the exact installed
	// inode back while that deletion is being replayed.
	if verifyRollbackCheckoutTarget(parent, cacheRoot, name, value, value.tempDevice, value.tempInode) == nil {
		return false, nil
	}
	if err := verifyNamedRecovery(parent, recovery.name, recovery); err != nil {
		return true, err
	}
	for ordinal := 1; ; ordinal++ {
		stat, err := statAt(parent, name)
		if errors.Is(err, fscompat.ENOENT) {
			// After a crashed or just-completed preservation rename, absence
			// is ours, not a local deletion. Restore the captured Base.
			if ordinal > 1 {
				journal, err = loadFSActions(ctx, db, worktree)
				if err != nil {
					return true, err
				}
			}
			for _, action := range journal {
				if sameRollbackFileReplacementParent(action, installed) && action.State == fsStateCompleted &&
					isFileReplacementSaveAction(action) && action.Source == name {
					return false, nil
				}
			}
			// Leave a local deletion for the next merge. Only discard the
			// verified, journal-owned copy of the old Sync Base.
			if err := journalRemoveAt(ctx, db, root, parent, worktree, fsPhaseRollback, installed.Parent, recovery.name,
				"File", recovery.name, recovery.id, recovery.mtime, recovery.size, recovery.device, recovery.inode, fault); err != nil {
				return true, err
			}
			return true, consumeSyncRecovery(ctx, db, recovery)
		}
		if err != nil {
			return true, err
		}
		if !statMatchesCheckoutTarget(stat, "File", installed.ParentDevice, stat.Ino) {
			return true, errors.New("concurrent checkout save is not a regular single-link file")
		}
		leaf, err := recoveryVisibleLeaf(installed.Parent, installed.ActionID, ordinal)
		if err != nil {
			return true, err
		}
		if _, err := statAt(parent, leaf); err == nil {
			continue
		} else if !errors.Is(err, fscompat.ENOENT) {
			return true, err
		}
		alias, err := parentHasCasefoldAlias(parent, leaf)
		if err != nil {
			return true, err
		}
		if alias {
			continue
		}
		if err := journalRenameAt(ctx, db, root, parent, worktree, fsPhaseRollback, installed.Parent, name, leaf,
			"File", "", "", uint64(stat.Dev), stat.Ino, fault); err != nil {
			return true, fmt.Errorf("preserve concurrent checkout save at %q: %w", joinScanPath(installed.Parent, leaf), err)
		}
		// A raced collision or a new save can leave the source occupied.
		// Recheck it before the captured Base is restored.
	}
}

// Reconcile a raced save rename without ever adopting or replacing its destination.
func reconcileFileReplacementSaveAction(ctx context.Context, db *sql.DB, parent *os.File, path string, action fsAction, journal []fsAction,
	sourceMatches, targetExists, targetMatches bool) (cancelled, alreadyMoved bool, err error) {
	if path != joinScanPath(action.Parent, action.Source) {
		return false, false, errors.New("save rename has no single-file checkout owner")
	}
	installed, _, err := fileReplacementInstall(ctx, db, action.Worktree, path, journal)
	if err != nil || installed.ActionID == "" || !sameRollbackFileReplacementParent(action, installed) {
		return false, false, errors.Join(errors.New("save rename has no durable single-file installation"), err)
	}
	if targetMatches && !sourceMatches {
		return false, true, nil // Rename ran; the editor may since have created another visible file.
	}
	if sourceMatches && !targetExists {
		alias, err := parentHasCasefoldAlias(parent, action.Target)
		if err != nil || !alias {
			return false, false, err
		}
	}
	// The intended identity is not present at the destination. Leave both
	// current paths alone; the rollback can select a new name or inode.
	result, err := db.ExecContext(ctx, `DELETE FROM fs_actions WHERE worktree=? AND action_id=? AND state='intent'
		AND EXISTS(SELECT 1 FROM pending_checkouts WHERE worktree=? AND apply_state='rolling_back')`,
		action.Worktree, action.ActionID, action.Worktree)
	if err != nil {
		return false, false, err
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return false, false, errors.Join(errors.New("could not cancel a raced save rename"), err)
	}
	return true, false, nil
}

// Applying can be interrupted after install but before the full target check.
// A resumed stage/install error must not strand a save or deletion in applying.
func rollbackChangedFileReplacementOnApplyError(ctx context.Context, db *sql.DB, options bindOptions, target checkoutPath,
	config libraryClientConfig, applyErr error) error {
	journal, err := loadFSActions(ctx, db, options.worktree)
	if err != nil {
		return errors.Join(applyErr, err)
	}
	installed, _, err := fileReplacementInstall(ctx, db, options.worktree, target.path, journal)
	if err != nil || installed.ActionID == "" {
		return errors.Join(applyErr, err)
	}
	parent, err := openFSActionParent(options.worktreeRoot, installed.Parent, installed.ParentDevice, installed.ParentInode)
	if err != nil {
		return errors.Join(applyErr, err)
	}
	expected := rollbackCheckoutPath{path: target.path, kind: "File", id: target.id, mtime: target.mtime, size: target.size}
	changed := verifyRollbackCheckoutTarget(parent, options.cacheRoot, installed.Target, expected, installed.ExpectedDevice, installed.ExpectedInode)
	if err := parent.Close(); err != nil || changed == nil {
		return errors.Join(applyErr, err)
	}
	return errors.Join(applyErr, rollbackApplyingCheckout(ctx, db, options, config))
}
