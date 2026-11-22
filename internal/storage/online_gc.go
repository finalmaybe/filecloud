package storage

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"
)

type gcScope struct{ owner, library string }

const transferQuietPeriod = 5 * time.Minute

var errGCYield = errors.New("online GC yielded to library activity")

// The registry lock covers lookup/lifetime only, never SQLite or disk I/O.
type gcActivities struct {
	mu    sync.Mutex
	gates map[gcScope]*gcGate
}
type gcGate struct {
	refs       int // under registry lock
	mu         sync.Mutex
	unlink     sync.Mutex // never hold the activity mutex during an unlink
	active     int
	quietUntil time.Time
	cancel     context.CancelCauseFunc
}

func (a *gcActivities) retain(scope gcScope) *gcGate {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.gates == nil {
		a.gates = make(map[gcScope]*gcGate)
	}
	g := a.gates[scope]
	if g == nil {
		g = &gcGate{}
		a.gates[scope] = g
	}
	g.refs++
	return g
}

// release is called with the gate locked. A recent transfer keeps a small entry
// until the next pass, but unsuccessful lookups of arbitrary IDs do not.
func (a *gcActivities) release(scope gcScope, g *gcGate) {
	a.mu.Lock()
	defer a.mu.Unlock()
	g.refs--
	if g.refs == 0 && !g.quietUntil.After(time.Now()) {
		delete(a.gates, scope)
	}
}

// UseLibrary protects the entire foreground operation, including validation through
// Head publication, or opening a Web version through streaming its last block.
func (s *Store) UseLibrary(owner, lib string) func() {
	return s.gcActivity.begin(gcScope{owner, lib}, false)
}

// UseTransfer additionally protects the gaps between requests during a sync. Only
// call this after verifying that the owner actually has this library.
func (s *Store) UseTransfer(owner, lib string) func() {
	return s.gcActivity.begin(gcScope{owner, lib}, true)
}
func (a *gcActivities) begin(scope gcScope, transfer bool) func() {
	g := a.retain(scope)
	g.mu.Lock()
	g.active++
	scanning := g.cancel != nil
	if scanning {
		g.cancel(errGCYield)
	}
	g.mu.Unlock()
	if scanning {
		// Cancel before waiting. Only an unlink already in progress can finish;
		// the collector cannot start another while this request is waiting.
		g.unlink.Lock()
		g.unlink.Unlock()
	}
	return func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		if transfer {
			g.quietUntil = time.Now().Add(transferQuietPeriod)
		}
		g.active--
		a.release(scope, g)
	}
}

// OnlineGCRound contains just the libraries still to visit. The scheduler owns
// one round at a time; retries do not rescan already collected libraries.
type OnlineGCRound struct {
	store   *Store
	pending []gcScope
}
type OnlineGCReport struct {
	Completed, Deferred, Failed, Objects int
	Bytes                                int64
}

func (s *Store) NewOnlineGCRound(ctx context.Context) (*OnlineGCRound, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT owner_user_id,id FROM libraries ORDER BY owner_user_id,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	r := &OnlineGCRound{store: s}
	for rows.Next() {
		var scope gcScope
		if err := rows.Scan(&scope.owner, &scope.library); err != nil {
			return nil, err
		}
		r.pending = append(r.pending, scope)
	}
	return r, rows.Err()
}

func (r *OnlineGCRound) Run(ctx context.Context) (OnlineGCReport, error) {
	var report OnlineGCReport
	var firstErr error
	retry := r.pending[:0]
	for _, scope := range r.pending {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		n, size, err := r.store.collectIdleLibrary(ctx, scope, time.Now())
		report.Objects += n
		report.Bytes += size
		switch {
		case errors.Is(err, errGCYield):
			retry = append(retry, scope)
			report.Deferred++
		case err != nil:
			report.Failed++
			if firstErr == nil {
				firstErr = err
			}
		default:
			report.Completed++
		}
	}
	r.pending = retry
	return report, firstErr
}

func (s *Store) collectIdleLibrary(ctx context.Context, scope gcScope, now time.Time) (count int, size int64, retErr error) {
	if !validObjectScopeID(scope.owner) || !validObjectScopeID(scope.library) {
		return 0, 0, errors.New("invalid GC library scope")
	}
	g := s.gcActivity.retain(scope)
	g.mu.Lock()
	if g.active != 0 || g.cancel != nil || now.Before(g.quietUntil) {
		s.gcActivity.release(scope, g)
		g.mu.Unlock()
		return 0, 0, errGCYield
	}
	scan, cancel := context.WithCancelCause(ctx)
	g.cancel = cancel
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		if cause := context.Cause(scan); cause != nil && (retErr == nil || errors.Is(retErr, context.Canceled)) {
			retErr = cause
		}
		cancel(nil)
		g.cancel = nil
		s.gcActivity.release(scope, g)
	}()
	collector := GarbageCollector{scope: &scope, db: s.db, objectsDir: s.objectsDir}
	plan, _, err := collector.collectionPlan(scan, now, MinimumGarbageCollectionGracePeriod)
	if err != nil {
		return 0, 0, err
	}
	if err := collector.seal(scan, now); err != nil {
		return 0, 0, err
	}
	for batch := range slices.Chunk(plan, 16) {
		n, bytes, err := s.unlinkGCBatch(scan, g, batch)
		count += n
		size += bytes
		if err != nil {
			return count, size, err
		}
	}
	return count, size, nil
}

// Sync directories outside the gate, including those affected by a partial batch.
func (s *Store) unlinkGCBatch(ctx context.Context, g *gcGate, batch []gcCandidate) (count int, size int64, err error) {
	parents := map[string]struct{}{}
	for _, item := range batch {
		g.unlink.Lock()
		err = context.Cause(ctx)
		if err == nil {
			err = os.Remove(item.path)
		}
		g.unlink.Unlock()
		if err != nil {
			break
		}
		count++
		size += item.size
		parents[filepath.Dir(item.path)] = struct{}{}
	}
	for parent := range parents {
		err = errors.Join(err, s.syncObjectDirectory(parent))
	}
	return count, size, err
}
