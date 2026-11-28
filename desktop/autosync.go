package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/rjeczalik/notify"
)

const fallbackSyncInterval = 30 * time.Second

// Each subscription belongs to one binding. Its goroutines only record hints;
// the scheduler retains the binding lock, pause, confirmation and retry rules.
type bindingWatch struct {
	worktree, token string
	stop            context.CancelFunc
	localAt         atomic.Int64
	remoteHead      atomic.Pointer[string]
}

func (s *Workspace) runAutoSync(ctx context.Context) {
	defer func() {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.stopWatches()
	}()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for ctx.Err() == nil {
		s.tick(ctx, time.Now())
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Workspace) updateWatches(ctx context.Context) {
	if s.locked() || ctx.Err() != nil {
		s.stopWatches()
		return
	}
	if s.watches == nil {
		s.watches = make(map[string]*bindingWatch)
	}
	for id, w := range s.watches {
		if !s.autoSyncEnabled(id) || w.worktree != s.bound[id].Worktree || w.token != s.token {
			w.stop()
			delete(s.watches, id)
		}
	}
	for id, b := range s.bound {
		if s.watches[id] != nil || !s.autoSyncEnabled(id) {
			continue
		}
		watchCtx, stop := context.WithCancel(ctx)
		w := &bindingWatch{worktree: b.Worktree, token: s.token, stop: stop}
		s.watches[id] = w
		go w.watchLocal(watchCtx)
		if s.token != "" {
			go w.watchRemote(watchCtx, s.active.Server, id, s.states[id].SyncBase)
		}
	}
}

func (s *Workspace) autoSyncEnabled(id string) bool {
	switch s.problems[id] {
	case "auth_required", "status_unavailable", "unsupported_fs":
		return false
	}
	pending := s.states[id].Pending
	return s.bound[id].Phase == "bound" && !s.data.Paused[id] && pending != "delete-confirmation" && pending != "restore-confirmation"
}

func (s *Workspace) stopWatches() {
	for _, w := range s.watches {
		w.stop()
	}
	clear(s.watches)
}

func (w *bindingWatch) changed(now time.Time, base string) bool {
	local := w.localAt.Load()
	remote := w.remoteHead.Load()
	return (local != 0 && now.Sub(time.UnixMilli(local)) >= 350*time.Millisecond) || (remote != nil && *remote != base)
}

func (w *bindingWatch) watchLocal(ctx context.Context) {
	events := make(chan notify.EventInfo, 32)
	defer notify.Stop(events)
	for notify.Watch(filepath.Join(w.worktree, "..."), events, notify.Create, notify.Write, notify.Rename, notify.Remove) != nil {
		select {
		case <-ctx.Done():
			return
		case <-time.After(fallbackSyncInterval):
		}
	}
	// Changes made while recursive watches were being installed need a scan too.
	w.localAt.Store(time.Now().UnixMilli())
	for {
		select {
		case <-ctx.Done():
			return
		case <-events:
			w.localAt.Store(time.Now().UnixMilli())
		}
	}
}

func (w *bindingWatch) watchRemote(ctx context.Context, server, id, after string) {
	endpoint, err := url.JoinPath(server, "v1/libraries", id, "head/changes")
	if err != nil {
		return
	}
	for ctx.Err() == nil {
		head, status := waitForHead(ctx, endpoint, w.token, after)
		switch {
		case status == http.StatusNoContent:
			continue
		case status == http.StatusOK && head != after:
			after = head
			w.remoteHead.Store(&head)
		case status == http.StatusUnauthorized:
			return // A fresh login starts a subscription with the new token.
		default:
			// Older servers and broken connections still have the regular sync timer.
			select {
			case <-ctx.Done():
				return
			case <-time.After(fallbackSyncInterval):
			}
		}
	}
}

func waitForHead(ctx context.Context, endpoint, token, after string) (string, int) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?after="+url.QueryEscape(after), nil)
	if err != nil {
		return "", 0
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := desktopHTTP.Do(req)
	if err != nil {
		return "", 0
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return "", res.StatusCode
	}
	var msg struct{ CommitId *string }
	if json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&msg) != nil || msg.CommitId == nil || (*msg.CommitId != "" && !validCommit(*msg.CommitId)) {
		return "", 0
	}
	return *msg.CommitId, res.StatusCode
}
