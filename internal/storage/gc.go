package storage

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mingming-cn/filecloud/internal/object"
)

const (
	_gcBeforeDelete = "before_delete"
	_gcAfterDelete  = "after_delete"

	// MinimumGarbageCollectionGracePeriod is the shortest safe age for unpublished objects.
	MinimumGarbageCollectionGracePeriod = 24 * time.Hour
)

// GarbageCollector holds the exclusive data-directory lock for one offline collection.
type GarbageCollector struct {
	scope      *gcScope // nil for whole-store offline maintenance
	db         *sql.DB
	objectsDir string
	lock       *dataLock
	remove     func(string) error
	syncDir    func(string) error
	fault      func(string) error
}

// GarbageCollectionOptions controls one offline collection.
type GarbageCollectionOptions struct {
	DryRun      bool
	GracePeriod time.Duration
	Now         time.Time
}

// GarbageCollectionObjectStats summarizes unreachable expired objects of one type.
type GarbageCollectionObjectStats struct {
	Type  string
	Count int
	Bytes int64
}

// GarbageCollectionReport summarizes the exact plan used by a dry-run or collection.
type GarbageCollectionReport struct {
	Objects []GarbageCollectionObjectStats
}

// OpenGarbageCollector acquires the exclusive data-directory lock before opening metadata or objects.
func OpenGarbageCollector(ctx context.Context, dataDir string) (*GarbageCollector, error) {
	lock, err := openDataLock(dataDir, false)
	if err != nil {
		return nil, err
	}
	if err := lock.exclusive(); err != nil {
		return nil, errors.Join(err, lock.Close())
	}
	info, err := os.Stat(dataDir)
	if err != nil {
		return nil, errors.Join(fmt.Errorf("open data directory: %w", err), lock.Close())
	}
	if !info.IsDir() {
		return nil, errors.Join(errors.New("data directory is not a directory"), lock.Close())
	}
	db, err := openDBMode(filepath.Join(dataDir, _databaseName), "rw")
	if err != nil {
		return nil, errors.Join(err, lock.Close())
	}
	return &GarbageCollector{
		db:         db,
		objectsDir: filepath.Join(dataDir, _objectsName),
		lock:       lock,
		remove:     os.Remove,
		syncDir:    syncDirectory,
	}, nil
}

// Close closes metadata and releases the exclusive data-directory lock.
func (g *GarbageCollector) Close() error {
	return errors.Join(g.db.Close(), g.lock.Close())
}

// Collect plans expired unreachable objects and deletes that exact plan unless DryRun is set.
func (g *GarbageCollector) Collect(ctx context.Context, options GarbageCollectionOptions) (GarbageCollectionReport, error) {
	if options.GracePeriod < MinimumGarbageCollectionGracePeriod {
		return GarbageCollectionReport{}, fmt.Errorf("gc grace period must be at least %s", MinimumGarbageCollectionGracePeriod)
	}
	if options.Now.IsZero() {
		options.Now = time.Now()
	}
	candidates, report, err := g.collectionPlan(ctx, options.Now, options.GracePeriod)
	if err != nil || options.DryRun {
		return report, err
	}
	// Persist the boundary before unlinking: later policy extensions cannot revive swept snapshots.
	if err = g.seal(ctx, options.Now); err != nil {
		return report, err
	}
	for _, candidate := range candidates {
		if err := ctx.Err(); err != nil {
			return GarbageCollectionReport{}, err
		}
		if err := g.runFault(_gcBeforeDelete); err != nil {
			return GarbageCollectionReport{}, err
		}
		if err := g.remove(candidate.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return GarbageCollectionReport{}, fmt.Errorf("delete %s object: %w", candidate.kind, err)
		}
		if err := g.syncDir(filepath.Dir(candidate.path)); err != nil {
			return GarbageCollectionReport{}, fmt.Errorf("sync collected %s object directory: %w", candidate.kind, err)
		}
		if err := g.runFault(_gcAfterDelete); err != nil {
			return GarbageCollectionReport{}, err
		}
	}
	return report, nil
}

func (g *GarbageCollector) collectionPlan(ctx context.Context, now time.Time, grace time.Duration) ([]gcCandidate, GarbageCollectionReport, error) {
	marked, err := g.markPublished(ctx, now)
	if err != nil {
		return nil, GarbageCollectionReport{}, err
	}
	return g.plan(ctx, marked, now.Add(-grace))
}

// Seal expiry before any unlink, so changing a policy or restarting after a partial sweep is safe.
func (g *GarbageCollector) seal(ctx context.Context, now time.Time) error {
	commits, args := g.scope.filter("p.owner_user_id", "p.library_id")
	trash, _ := g.scope.filter("owner", "library")
	const batch = 128
	args = append(append([]any{now.Unix()}, args...), batch)
	for {
		tx, err := g.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		pruned, err := tx.ExecContext(ctx, `UPDATE published_commits SET pruned=1 WHERE (owner_user_id,library_id,commit_id) IN (
		SELECT p.owner_user_id,p.library_id,p.commit_id `+expiryFrom+` WHERE p.pruned=0 AND `+expiredPredicate+commits+` LIMIT ?)`, args...)
		if err != nil {
			tx.Rollback()
			return err
		}
		expired, err := tx.ExecContext(ctx, `DELETE FROM trash_entries WHERE id IN (SELECT id FROM trash_lifetimes WHERE keep_until<=?`+trash+` LIMIT ?)`, args...)
		if err != nil {
			tx.Rollback()
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}
		n, err := pruned.RowsAffected()
		if err != nil {
			return err
		}
		m, err := expired.RowsAffected()
		if err != nil {
			return err
		}
		if n < batch && m < batch {
			return nil
		}
	}
}

func (scope *gcScope) filter(ownerColumn, libraryColumn string) (string, []any) {
	if scope == nil {
		return "", nil
	}
	return " AND " + ownerColumn + "=? AND " + libraryColumn + "=?", []any{scope.owner, scope.library}
}

func openReadOnlyDB(ctx context.Context, path string) (*sql.DB, error) {
	u := &url.URL{Scheme: "file", Path: sqliteURLPath(path)}
	query := u.Query()
	query.Set("mode", "ro")
	query.Add("_pragma", "foreign_keys(1)")
	query.Add("_pragma", fmt.Sprintf("busy_timeout(%d)", _busyTimeoutMillis))
	u.RawQuery = query.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, fmt.Errorf("open metadata database read-only: %w", err)
	}
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		return nil, errors.Join(fmt.Errorf("ping metadata database read-only: %w", err), db.Close())
	}
	return db, nil
}

type gcObjectKey struct {
	owner, library, kind, id string
}

type gcRoot struct {
	owner, library, id string
}

type gcCandidate struct {
	path string
	kind string
	size int64
}

func (g *GarbageCollector) markPublished(ctx context.Context, now time.Time) (map[gcObjectKey]struct{}, error) {
	roots, err := g.publishedRoots(ctx)
	if err != nil {
		return nil, err
	}
	filter, args := g.scope.filter("p.owner_user_id", "p.library_id")
	expired, err := expiredRoots(ctx, g.db, `SELECT p.owner_user_id,p.library_id,p.commit_id `+expiryFrom+` WHERE `+expiredPredicate+filter, append([]any{now.Unix()}, args...)...)
	if err != nil {
		return nil, err
	}
	marked := make(map[gcObjectKey]struct{})
	metadata := make(map[gcObjectKey]struct{})
	for _, root := range roots {
		if !validObjectScopeID(root.owner) || !validObjectScopeID(root.library) || !object.ValidID(root.id) {
			return nil, errors.New("invalid published object location")
		}
		if err := g.markCommitGraph(ctx, marked, metadata, expired, root); err != nil {
			return nil, err
		}
	}
	pins, err := trashRoots(ctx, g.db, now, g.scope)
	if err != nil {
		return nil, err
	}
	for _, p := range pins {
		if p.kind == "Directory" {
			err = g.markSnapshot(ctx, marked, p.owner, p.library, p.id, true)
		} else {
			err = g.markFile(ctx, marked, p.owner, p.library, p.id, true)
		}
		if err != nil {
			return nil, err
		}
	}
	// Merge metadata only after pinning content, since the mark set deduplicates whole subtrees.
	for key := range metadata {
		marked[key] = struct{}{}
	}
	return marked, nil
}

func (g *GarbageCollector) publishedRoots(ctx context.Context) (roots []gcRoot, retErr error) {
	heads, args := g.scope.filter("owner_user_id", "id")
	commits, more := g.scope.filter("owner_user_id", "library_id")
	rows, err := g.db.QueryContext(ctx, `
		SELECT owner_user_id, id, head_commit_id
		FROM libraries WHERE head_commit_id IS NOT NULL`+heads+`
		UNION
		SELECT owner_user_id, library_id, commit_id
		FROM published_commits WHERE 1=1`+commits+`
		ORDER BY 1, 2, 3`, append(args, more...)...)
	if err != nil {
		return nil, fmt.Errorf("read published commit roots: %w", err)
	}
	defer func() { retErr = errors.Join(retErr, rows.Close()) }()
	for rows.Next() {
		var root gcRoot
		if err := rows.Scan(&root.owner, &root.library, &root.id); err != nil {
			return nil, fmt.Errorf("scan published commit root: %w", err)
		}
		roots = append(roots, root)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate published commit roots: %w", err)
	}
	return roots, nil
}

func (g *GarbageCollector) markCommitGraph(ctx context.Context, marked, metadata map[gcObjectKey]struct{}, expired map[gcRoot]bool, root gcRoot) error {
	commits := []string{root.id}
	for len(commits) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		id := commits[len(commits)-1]
		commits = commits[:len(commits)-1]
		key := gcObjectKey{owner: root.owner, library: root.library, kind: "commits", id: id}
		if _, exists := marked[key]; exists {
			continue
		}
		data, err := g.readPublishedObject(key)
		if err != nil {
			return err
		}
		commit, err := object.VerifyCommit(data, id)
		if err != nil {
			return fmt.Errorf("verify published commit: %w", err)
		}
		marked[key] = struct{}{}
		commits = append(commits, commit.Parents...)
		target := marked
		keepContent := !expired[gcRoot{root.owner, root.library, id}]
		if !keepContent {
			target = metadata
		}
		if err := g.markSnapshot(ctx, target, root.owner, root.library, commit.Root, keepContent); err != nil {
			return err
		}
	}
	return nil
}

func (g *GarbageCollector) markSnapshot(ctx context.Context, marked map[gcObjectKey]struct{}, owner, library, rootID string, keepContent bool) error {
	directories := []string{rootID}
	for len(directories) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		id := directories[len(directories)-1]
		directories = directories[:len(directories)-1]
		key := gcObjectKey{owner: owner, library: library, kind: "directories", id: id}
		if _, exists := marked[key]; exists {
			continue
		}
		data, err := g.readPublishedObject(key)
		if err != nil {
			return err
		}
		directory, err := object.VerifyDirectory(data, id)
		if err != nil {
			return fmt.Errorf("verify published directory: %w", err)
		}
		marked[key] = struct{}{}
		for _, entry := range directory.Entries {
			switch entry.Type {
			case "Directory":
				directories = append(directories, entry.ID)
			case "File":
				if err := g.markFile(ctx, marked, owner, library, entry.ID, keepContent); err != nil {
					return err
				}
			default:
				return errors.New("invalid published directory entry type")
			}
		}
	}
	return nil
}

func (g *GarbageCollector) markFile(ctx context.Context, marked map[gcObjectKey]struct{}, owner, library, id string, keepContent bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key := gcObjectKey{owner: owner, library: library, kind: "files", id: id}
	if _, exists := marked[key]; exists {
		return nil
	}
	data, err := g.readPublishedObject(key)
	if err != nil {
		return err
	}
	file, err := object.VerifyFile(data, id)
	if err != nil {
		return fmt.Errorf("verify published file: %w", err)
	}
	marked[key] = struct{}{}
	if !keepContent {
		return nil
	}
	for _, blockID := range file.Blocks {
		marked[gcObjectKey{owner: owner, library: library, kind: "blocks", id: blockID}] = struct{}{}
	}
	return nil
}

func (g *GarbageCollector) readPublishedObject(key gcObjectKey) ([]byte, error) {
	if err := validateObjectLocation(g.objectsDir, key.owner, key.library, key.kind, key.id); err != nil {
		return nil, fmt.Errorf("locate published %s object: %w", key.kind, err)
	}
	path := filepath.Join(g.objectsDir, key.owner, key.library, key.kind, key.id[:2], key.id[2:])
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("stat published %s object: %w", key.kind, err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("published %s object is not a regular file", key.kind)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read published %s object: %w", key.kind, err)
	}
	return data, nil
}

func (g *GarbageCollector) plan(ctx context.Context, marked map[gcObjectKey]struct{}, cutoff time.Time) ([]gcCandidate, GarbageCollectionReport, error) {
	kinds := []string{"blocks", "files", "directories", "commits"}
	stats := make(map[string]*GarbageCollectionObjectStats, len(kinds))
	report := GarbageCollectionReport{Objects: make([]GarbageCollectionObjectStats, len(kinds))}
	for i, kind := range kinds {
		report.Objects[i].Type = kind
		stats[kind] = &report.Objects[i]
	}
	var candidates []gcCandidate
	start := g.objectsDir
	if g.scope != nil {
		for _, part := range []string{g.scope.owner, g.scope.library} {
			start = filepath.Join(start, part)
			info, err := os.Lstat(start)
			if errors.Is(err, fs.ErrNotExist) {
				return nil, report, nil
			}
			if err != nil {
				return nil, report, err
			}
			if !info.IsDir() {
				return nil, report, errors.New("invalid library object directory")
			}
		}
	}
	err := filepath.WalkDir(start, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(g.objectsDir, path)
		if err != nil {
			return fmt.Errorf("locate stored object: %w", err)
		}
		parts := strings.Split(relative, string(filepath.Separator))
		if len(parts) != 5 || !validObjectScopeID(parts[0]) || !validObjectScopeID(parts[1]) {
			return nil
		}
		kind, prefix, leaf := parts[2], parts[3], parts[4]
		kindStats := stats[kind]
		if kindStats == nil || len(prefix) != 2 {
			return nil
		}
		id := prefix + leaf
		isTemporary := strings.HasPrefix(leaf, ".filecloud-object-")
		if !isTemporary && !object.ValidID(id) {
			return nil
		}
		if !isTemporary {
			key := gcObjectKey{owner: parts[0], library: parts[1], kind: kind, id: id}
			if _, reachable := marked[key]; reachable {
				return nil
			}
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat stored %s object: %w", kind, err)
		}
		if !info.Mode().IsRegular() || info.ModTime().After(cutoff) {
			return nil
		}
		candidates = append(candidates, gcCandidate{path: path, kind: kind, size: info.Size()})
		kindStats.Count++
		kindStats.Bytes += info.Size()
		return nil
	})
	if err != nil {
		return nil, GarbageCollectionReport{}, fmt.Errorf("scan stored objects: %w", err)
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].path < candidates[j].path })
	return candidates, report, nil
}

func (g *GarbageCollector) runFault(point string) error {
	if g.fault == nil {
		return nil
	}
	if err := g.fault(point); err != nil {
		return fmt.Errorf("gc %s: %w", point, err)
	}
	return nil
}
