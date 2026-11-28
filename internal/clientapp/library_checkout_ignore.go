package clientapp

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mingming-cn/filecloud/internal/fscompat"
	"github.com/mingming-cn/filecloud/internal/object"
	"github.com/mingming-cn/filecloud/internal/syncignore"
	"golang.org/x/text/cases"
)

const checkoutIgnorePlanSchema = `CREATE TABLE checkout_ignore_plans (
 worktree TEXT PRIMARY KEY NOT NULL REFERENCES pending_checkouts(worktree) ON DELETE CASCADE,
 rules_json TEXT NOT NULL)`
const checkoutRetainedDirectorySchema = `CREATE TABLE checkout_retained_directories (
 worktree TEXT NOT NULL REFERENCES checkout_ignore_plans(worktree) ON DELETE CASCADE,
 path TEXT NOT NULL, device INTEGER NOT NULL, inode INTEGER NOT NULL, mtime TEXT NOT NULL,
 PRIMARY KEY(worktree, path))`

func migrateCheckoutIgnoreSchema(ctx context.Context, tx *sql.Tx) error {
	for _, statement := range []string{checkoutIgnorePlanSchema, checkoutRetainedDirectorySchema} {
		if _, err := tx.ExecContext(ctx, strings.Replace(statement, "CREATE TABLE", "CREATE TABLE IF NOT EXISTS", 1)); err != nil {
			return err
		}
	}
	return validateCheckoutIgnoreSchema(ctx, tx)
}

func validateCheckoutIgnoreSchema(ctx context.Context, db clientSchemaQuerier) error {
	for table, want := range map[string]string{"checkout_ignore_plans": checkoutIgnorePlanSchema, "checkout_retained_directories": checkoutRetainedDirectorySchema} {
		var schema string
		if err := db.QueryRowContext(ctx, `SELECT sql FROM sqlite_schema WHERE type='table' AND name=?`, table).Scan(&schema); err != nil || canonicalSQLiteSQL(schema) != canonicalSQLiteSQL(want) {
			return errors.Join(errors.New("invalid sync ignore checkout schema"), err)
		}
		var triggers int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_schema WHERE type='trigger' AND tbl_name=?`, table).Scan(&triggers); err != nil || triggers != 0 {
			return errors.Join(errors.New("invalid sync ignore checkout triggers"), err)
		}
	}
	return nil
}

// Omitted paths keep their last observed local object, not the current remote
// one. This distinguishes a stale local copy from an edit when rules are lifted.
func loadIgnorePathIndex(ctx context.Context, db *sql.DB, worktree string, rules syncignore.Rules) (map[string]checkoutPath, error) {
	rows, err := db.QueryContext(ctx, `SELECT path,type,object_id,canonical_mtime,actual_mtime,size FROM path_index
  WHERE worktree=? AND (? OR actual_mtime='')`, worktree, rules.Active())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	paths := make(map[string]checkoutPath)
	for rows.Next() {
		var value checkoutPath
		var actual string
		if err := rows.Scan(&value.path, &value.kind, &value.id, &value.mtime, &actual, &value.size); err != nil {
			return nil, err
		}
		value.unmaterialized = actual == ""
		if value.unmaterialized || ignoreSyncPath(rules, value.path) {
			paths[value.path] = value
		}
	}
	return paths, rows.Err()
}

func omittedForIndex(omitted []checkoutPath, config worktreeScanConfig) []checkoutPath {
	result := make([]checkoutPath, 0, len(omitted))
	seen := make(map[string]bool, len(omitted))
	for _, value := range omitted {
		seen[value.path] = true
		if prior, ok := config.ignoreIndex[value.path]; ok {
			value = prior
		} else {
			value.mtime = ""
		}
		value.unmaterialized = true
		result = append(result, value)
	}
	for path, value := range config.ignoreIndex {
		if !seen[path] && ignoreSyncPath(config.ignore, path) {
			value.unmaterialized = true
			result = append(result, value)
		}
	}
	return result
}

func participatingTrackedPaths(config worktreeScanConfig) map[string]bool {
	if !config.ignore.Active() {
		return config.trackedPaths
	}
	paths := make(map[string]bool)
	for path := range config.trackedPaths {
		if !ignoreSyncPath(config.ignore, path) {
			paths[path] = true
		}
	}
	return paths
}

func ignoreRulesID(rules syncignore.Rules) string {
	if !rules.Active() {
		return ""
	}
	data, _ := json.Marshal(rules)
	return object.ID(data)
}

// Resume any partially written checkout with its original rules. A settings change
// applies to the next round, after the recovery journal has been cleared.
func checkoutIgnoreRules(ctx context.Context, db *sql.DB, worktree string, current syncignore.Rules) (syncignore.Rules, error) {
	var state, pinned string
	var hasPaths bool
	err := db.QueryRowContext(ctx, `SELECT c.apply_state, COALESCE(p.rules_json,''),
		EXISTS(SELECT 1 FROM checkout_paths WHERE worktree=c.worktree)
		FROM pending_checkouts c LEFT JOIN checkout_ignore_plans p USING(worktree) WHERE c.worktree=?`, worktree).Scan(&state, &pinned, &hasPaths)
	if errors.Is(err, sql.ErrNoRows) {
		return current, nil
	}
	if err != nil {
		return syncignore.Rules{}, err
	}
	if pinned == "" && state == "pending" && !hasPaths {
		return current, nil
	}
	var saved syncignore.Rules
	if pinned != "" {
		if err := json.Unmarshal([]byte(pinned), &saved); err != nil {
			return saved, err
		}
		return saved, saved.Validate()
	}
	return saved, nil // Unfiltered checkouts have no ignore plan.
}

type retainedCheckoutDirectory struct {
	path          string
	device, inode uint64
	mtime         string
}

func loadRetainedCheckoutDirectories(ctx context.Context, db fsActionQueryer, worktree string) (map[string]retainedCheckoutDirectory, error) {
	rows, err := db.QueryContext(ctx, `SELECT path,device,inode,mtime FROM checkout_retained_directories WHERE worktree=?`, worktree)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	directories := make(map[string]retainedCheckoutDirectory)
	for rows.Next() {
		var dir retainedCheckoutDirectory
		if err := rows.Scan(&dir.path, &dir.device, &dir.inode, &dir.mtime); err != nil {
			return nil, err
		}
		mtime, err := time.Parse(time.RFC3339Nano, dir.mtime)
		if !_validConflictPromotionPath(dir.path) || dir.device == 0 || dir.inode == 0 || err != nil || mtime.UTC().Format(time.RFC3339Nano) != dir.mtime {
			return nil, errors.New("invalid retained checkout directory identity")
		}
		directories[dir.path] = dir
	}
	return directories, rows.Err()
}

// Pin the rule set and protected parent identities before any checkout mutation.
func prepareCheckoutIgnorePlan(ctx context.Context, db *sql.DB, options *bindOptions, targets []checkoutPath, snapshot worktreeSnapshot) error {
	if !options.scanConfig.ignore.Active() {
		return nil
	}
	var planned bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM checkout_ignore_plans WHERE worktree=?)`, options.worktree).Scan(&planned); err != nil {
		return err
	}
	if planned {
		dirs, err := loadRetainedCheckoutDirectories(ctx, db, options.worktree)
		options.retainedDirs = dirs
		return err
	}
	if err := validateIgnoreCheckoutTargets(snapshot.ignored, targets); err != nil {
		return err
	}
	protected := ignoredAncestors(snapshot.ignored)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	fail := func(err error) error { return errors.Join(err, tx.Rollback()) }
	rulesJSON, err := json.Marshal(options.scanConfig.ignore)
	if err != nil {
		return fail(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO checkout_ignore_plans(worktree,rules_json) VALUES (?,?)`, options.worktree, string(rulesJSON)); err != nil {
		return fail(err)
	}
	for path := range protected {
		parent, name, err := openCheckoutParent(options.worktreeRoot, path, nil)
		if err != nil {
			return fail(err)
		}
		stat, err := statAt(parent, name)
		if err = errors.Join(err, parent.Close()); err != nil {
			return fail(err)
		}
		if stat.Mode&fscompat.S_IFMT != fscompat.S_IFDIR || uint64(stat.Dev) != options.worktreeRoot.device {
			return fail(errSyncIgnoreBlocked)
		}
		mtime := time.Unix(stat.Mtim.Sec, stat.Mtim.Nsec).UTC().Format(time.RFC3339Nano)
		if _, err := tx.ExecContext(ctx, `INSERT INTO checkout_retained_directories(worktree,path,device,inode,mtime) VALUES (?,?,?,?,?)`, options.worktree, path, uint64(stat.Dev), stat.Ino, mtime); err != nil {
			return fail(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	options.retainedDirs, err = loadRetainedCheckoutDirectories(ctx, db, options.worktree)
	return err
}

func validateIgnoreCheckoutTargets(ignored map[string]bool, targets []checkoutPath) error {
	if len(ignored) == 0 {
		return nil
	}
	protected := ignoredAncestors(ignored)
	fold := cases.Fold()
	blocked := make(map[string]string, len(protected)+len(ignored))
	for path := range ignored {
		blocked[fold.String(path)] = path
	}
	for path := range protected {
		blocked[fold.String(path)] = path
	}
	for _, target := range targets {
		if path, ok := blocked[fold.String(target.path)]; ok && (path != target.path || target.kind != "Directory" || !protected[path]) {
			return errSyncIgnoreBlocked
		}
	}
	return nil
}

func adoptRetainedCheckoutDirectory(ctx context.Context, db *sql.DB, options bindOptions, value checkoutPath, dir retainedCheckoutDirectory) error {
	opened, err := openFSActionParent(options.worktreeRoot, dir.path, dir.device, dir.inode)
	if err != nil {
		return err
	}
	if err := opened.Close(); err != nil {
		return err
	}
	result, err := db.ExecContext(ctx, `UPDATE checkout_paths SET target_device=?,target_inode=?,completed=1,actual_mtime=?
		WHERE worktree=? AND path=? AND type='Directory' AND temp_name='' AND
		((target_device=0 AND target_inode=0) OR (target_device=? AND target_inode=?))`, dir.device, dir.inode, value.mtime,
		options.worktree, value.path, dir.device, dir.inode)
	if err != nil {
		return err
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return errors.Join(errors.New("retained checkout directory did not match target intent"), err)
	}
	return nil
}

func verifyCheckoutView(options bindOptions, targetRoot string, targets []checkoutPath) (worktreeSnapshot, error) {
	snapshot, err := scanWorktreeWithConfig(options.worktreeRoot, options.scanConfig)
	if err != nil {
		return snapshot, err
	}
	if !options.scanConfig.ignore.Active() {
		if snapshot.root != targetRoot {
			return worktreeSnapshot{}, errors.New("worktree did not match its fixed target")
		}
		return snapshot, nil
	}
	if err := validateIgnoreCheckoutTargets(snapshot.ignored, targets); err != nil {
		return worktreeSnapshot{}, err
	}
	actual := make(map[string]checkoutPath, len(snapshot.paths))
	for _, value := range snapshot.paths {
		actual[value.path] = value
	}
	for _, target := range targets {
		value, ok := actual[target.path]
		if !ok || value.kind != target.kind || value.kind == "File" && (value.id != target.id || value.mtime != target.mtime) {
			return worktreeSnapshot{}, fmt.Errorf("worktree path %q did not match its fixed target", target.path)
		}
		if retained, ok := options.retainedDirs[value.path]; ok {
			if retained.device != value.device || retained.inode != value.inode {
				return worktreeSnapshot{}, errSyncIgnoreBlocked
			}
		}
		if _, retained := options.retainedDirs[value.path]; value.kind == "Directory" && target.mtime != value.mtime &&
			(!retained || isCheckoutAncestor(value.path, options.syncFileReplacement)) {
			return worktreeSnapshot{}, fmt.Errorf("worktree directory %q did not match its fixed target mtime", target.path)
		}
		delete(actual, target.path)
	}
	for path, value := range actual {
		retained, ok := options.retainedDirs[path]
		if !ok || value.kind != "Directory" || retained.device != value.device || retained.inode != value.inode {
			return worktreeSnapshot{}, fmt.Errorf("unexpected checkout path %q", path)
		}
	}
	return snapshot, nil
}

func joinCheckoutPaths(left, right map[string]bool) map[string]bool {
	joined := make(map[string]bool, len(left)+len(right))
	for path := range left {
		joined[path] = true
	}
	for path := range right {
		joined[path] = true
	}
	return joined
}

func recoveryStoredPath(value syncRecovery, name string) string {
	parent, _ := splitFSActionPath(value.path)
	return joinScanPath(parent, name)
}

func openSyncRecoveryParent(ctx context.Context, db *sql.DB, root *openedWorktree, value syncRecovery) (*os.File, string, string, error) {
	parent, _ := splitFSActionPath(value.path)
	var dirs map[string]retainedCheckoutDirectory
	if parent != "" {
		var dir retainedCheckoutDirectory
		err := db.QueryRowContext(ctx, `SELECT device,inode FROM checkout_retained_directories WHERE worktree=? AND path=?`, value.worktree, parent).Scan(&dir.device, &dir.inode)
		if err != nil {
			return nil, "", "", err
		}
		dirs = map[string]retainedCheckoutDirectory{parent: dir}
	}
	return openSyncRecoveryParentAt(root, dirs, value)
}

func openSyncRecoveryParentAt(root *openedWorktree, dirs map[string]retainedCheckoutDirectory, value syncRecovery) (*os.File, string, string, error) {
	parent, leaf := splitFSActionPath(value.path)
	if !_validConflictPromotionPath(value.path) || value.name != "" && (!strings.HasPrefix(value.name, syncRecoveryPrefix) || !validFSActionID(strings.TrimPrefix(value.name, syncRecoveryPrefix))) ||
		value.tombstone != "" && (!strings.HasPrefix(value.tombstone, syncTombstonePrefix) || !validFSActionID(strings.TrimPrefix(value.tombstone, syncTombstonePrefix))) {
		return nil, "", "", errors.New("sync recovery contains an invalid path")
	}
	device, inode := root.device, root.inode
	if parent != "" {
		dir, ok := dirs[parent]
		if !ok || dir.device == 0 || dir.inode == 0 {
			return nil, "", "", errors.New("nested sync recovery has no retained parent identity")
		}
		device, inode = dir.device, dir.inode
	}
	opened, err := openFSActionParent(root, parent, device, inode)
	return opened, parent, leaf, err
}

func restoreRetainedDirectoryMtimes(ctx context.Context, db *sql.DB, root *openedWorktree, worktree, replacement string, fault fsActionFault) error {
	directories, err := loadRetainedCheckoutDirectories(ctx, db, worktree)
	if err != nil {
		return err
	}
	for path, dir := range directories {
		if replacement != "" {
			if isCheckoutAncestor(path, replacement) {
				if err := setFileReplacementDirectoryMtime(ctx, db, root, worktree, fsPhaseRollback,
					checkoutPath{path: path, mtime: dir.mtime}, dir, fault); err != nil {
					return err
				}
			}
			continue
		}
		parent, leaf := splitFSActionPath(path)
		if err := journalMtime(ctx, db, root, worktree, fsPhaseRollback, parent, leaf, "Directory", dir.mtime, dir.device, dir.inode, fault); err != nil {
			return err
		}
	}
	return nil
}

func scanForInitialIgnorePlan(ctx context.Context, db *sql.DB, options bindOptions) (worktreeSnapshot, error) {
	var exists bool
	if err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM checkout_ignore_plans WHERE worktree=?)`, options.worktree).Scan(&exists); err != nil || exists {
		return worktreeSnapshot{}, err
	}
	return scanWorktreeWithConfig(options.worktreeRoot, options.scanConfig)
}
