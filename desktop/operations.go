package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"slices"
	"time"
)

func (s *Workspace) PickDirectory(id string) (string, error) {
	s.mu.Lock()
	s.selectionID = ""
	owner := s.active
	if s.locked() || s.token == "" {
		s.mu.Unlock()
		return "", errSession
	}
	if _, ok := s.data.Names[id]; !ok || s.bound[id].Phase != "" {
		s.mu.Unlock()
		return "", errBinding
	}
	s.mu.Unlock()
	path, err := s.pick()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.locked() || owner != s.active {
		return "", errSession
	}
	if err != nil || path == "" {
		return "", err
	}
	s.selectionID = id
	s.selection = choice{Worktree: path}
	return path, nil
}
func (s *Workspace) Bind(id, path string, confirmLocal bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.publish()
	if s.locked() || s.token == "" {
		return errSession
	}
	if id != s.selectionID || path != s.selection.Worktree || path == "" {
		return errors.New("请重新选择工作目录。")
	}
	if s.selection.MergeCandidate != "" && !confirmLocal {
		return errors.New("请先确认和已有文件夹同步。")
	}
	s.selectionID = ""
	s.selection.Import = confirmLocal && s.selection.MergeCandidate == ""
	s.data.Intents[id] = s.selection
	if err := s.save(); err != nil {
		return err
	}
	return s.bind(id, s.data.Intents[id], s.data.Device)
}
func (s *Workspace) RetryBinding(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.publish()
	if s.locked() || s.token == "" {
		return errSession
	}
	if b := s.bound[id]; b.Phase == "bound" {
		return s.bind(id, choice{Worktree: b.Worktree}, b.DeviceId)
	}
	c, ok := s.data.Intents[id]
	if !ok {
		return errBinding
	}
	return s.bind(id, c, s.data.Device)
}
func (s *Workspace) bindArgs(id, path, device string) []string {
	return []string{"library", "bind", "--server", s.active.Server, "--library-id", id, "--device-id", device, "--client-dir", s.clientDir(), "--worktree", path, "--token-stdin"}
}
func (s *Workspace) bind(id string, c choice, device string) error {
	args := s.bindArgs(id, c.Worktree, device)
	if c.MergeCandidate != "" {
		args = append(args, "--merge-existing", c.MergeCandidate)
	}
	if c.Import {
		args = append(args, "--import-local")
	}
	if err := s.query(nil, s.token, args...); err != nil {
		if c.MergeCandidate != "" {
			delete(s.data.Intents, id)
			if saveErr := s.save(); saveErr != nil {
				return saveErr
			}
		}
		_ = s.loadBindings()
		if code, ok := err.(commandError); ok && code != "unknown" {
			return err
		}
		return errors.New("同步设置未完成。请检查文件夹和登录状态，然后重试。")
	}
	delete(s.data.Intents, id)
	delete(s.problems, id)
	delete(s.due, id)
	if err := s.loadBindings(); err != nil {
		return err
	}
	if err := s.save(); err != nil {
		return err
	}
	if c.MergeCandidate != "" {
		return s.syncOne(id, "")
	}
	return nil
}
func (s *Workspace) OpenWorktree(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.bound[id]
	if s.locked() || b.Phase != "bound" {
		return errBinding
	}
	if stat, err := os.Stat(b.Worktree); err != nil || !stat.IsDir() {
		return errors.New("工作目录不可用。")
	}
	return s.open(b.Worktree)
}
func (s *Workspace) Unbind(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.publish()
	if s.locked() {
		return errSession
	}
	b := s.bound[id]
	if b.Phase != "bound" {
		return errBinding
	}
	if err := s.query(nil, "", "library", "unbind", "--if-idle", "--client-dir", s.clientDir(), "--worktree", b.Worktree); err != nil {
		return errors.New("解除同步未完成；请先处理未完成的任务。")
	}
	delete(s.bound, id)
	delete(s.states, id)
	delete(s.problems, id)
	delete(s.data.Paused, id)
	delete(s.data.LastSuccess, id)
	delete(s.data.Conflicts, id)
	delete(s.data.Intents, id)
	s.review = nil
	return s.save()
}
func (s *Workspace) SetPaused(id string, paused bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.publish()
	if s.locked() || s.bound[id].Phase != "bound" {
		return errBinding
	}
	s.data.Paused[id] = paused
	delete(s.due, id)
	return s.save()
}
func (s *Workspace) ReviewDeletion(id string) (*Review, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.publish()
	if s.locked() || s.bound[id].Phase != "bound" {
		return nil, errBinding
	}
	return s.reviewDeletion(id)
}
func (s *Workspace) reviewDeletion(id string) (*Review, error) {
	st, err := s.readStatus(s.bound[id])
	if err != nil {
		return nil, err
	}
	s.review = nil
	if st.Pending != "delete-confirmation" || st.Confirmation == nil || st.Deletion == nil {
		return nil, errors.New("删除候选已变化，请重新查看状态。")
	}
	v := Review{id, st.Worktree, st.Confirmation.CandidatePrefix, st.Deletion.Deleted, st.Deletion.Tracked}
	s.review = &v
	return &v, nil
}
func (s *Workspace) CancelReview() { s.mu.Lock(); defer s.mu.Unlock(); s.review = nil }

func (s *Workspace) ConfirmDeletion(v Review) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.publish()
	if s.locked() || s.review == nil || *s.review != v {
		return commandError("stale_candidate")
	}
	current, err := s.reviewDeletion(v.ID)
	s.review = nil
	if err != nil || *current != v {
		return commandError("stale_candidate")
	}
	return s.syncOne(v.ID, v.Candidate)
}
func (s *Workspace) RequestSync(id string) error {
	if !s.mu.TryLock() {
		return errors.New("已有操作正在进行。")
	}
	defer s.mu.Unlock()
	if s.locked() || s.bound[id].Phase != "bound" {
		return errBinding
	}
	return s.syncOne(id, "")
}
func (s *Workspace) syncOne(id, prefix string) error {
	b := s.bound[id]
	st, err := s.readStatus(b)
	if err != nil {
		s.publish()
		return err
	}
	if prefix == "" && st.Pending == "restore-confirmation" {
		s.publish()
		return errors.New("此资料库需要先核对待办。")
	}
	// Consume hints before the scan so edits arriving during sync survive this round.
	if w := s.watches[id]; w != nil {
		w.localAt.Store(0)
		w.remoteHead.Store(nil)
	}
	s.running = id
	s.scanDone = false
	s.publish()
	defer func() { s.running = ""; s.publish() }()
	args := []string{"library", "sync", "--output=jsonl", "--conflict-paths", "--client-dir", s.clientDir(), "--worktree", b.Worktree}
	if prefix != "" {
		args = append(args, "--confirm-delete", prefix)
	}
	cmd := s.cliCommand(args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	runErr := cmd.Start()
	var result, code string
	var scanned, conflictAdded bool
	if runErr == nil {
		dec := json.NewDecoder(stdout)
		for {
			var msg struct{ Type, Outcome, Code, Source, Target string }
			err := dec.Decode(&msg)
			if err == io.EOF {
				break
			}
			if err != nil || result != "" {
				result = ""
				break
			}
			switch msg.Type {
			case "conflict_saved":
				item := Conflict{msg.Source, msg.Target}
				if !slices.Contains(s.data.Conflicts[id], item) {
					s.data.Conflicts[id] = append(s.data.Conflicts[id], item)
					conflictAdded = true
					s.activity(id, "conflict_saved", time.Now())
				}
			case "scan_completed":
				scanned = true
				s.scanDone = true
				s.publish()
			case "result":
				result, code = msg.Outcome, msg.Code
			}
		}
		stdout.Close()
		runErr = cmd.Wait()
	}
	switch {
	case runErr == nil && (result == "ok" || result == "no_change"):
		code = ""
	case result == "needs_confirmation":
		code = "delete_confirmation"
	case result != "failed" || code == "":
		code = "interrupted"
	}
	now := time.Now()
	delay := fallbackSyncInterval
	if code == "offline" || code == "retry_later" {
		delay = max(delay, min(5*time.Minute, 2*s.delays[id]))
	}
	s.delays[id] = delay
	s.due[id] = now.Add(delay)
	if code == "" {
		s.data.LastSuccess[id] = now.UTC().Format(time.RFC3339Nano)
	}
	old := s.problems[id]
	s.problems[id] = code
	lastUnchanged := false
	if result == "no_change" {
		for i := len(s.data.Activity) - 1; i >= 0; i-- {
			if a := s.data.Activity[i]; a.LibraryID == id {
				lastUnchanged = a.Kind == "no_change"
				break
			}
		}
	}
	if !lastUnchanged && (code == "" || old != code) {
		if scanned && result != "no_change" {
			s.activity(id, "scan_completed", now)
		}
		kind := code
		if kind == "" {
			kind = "completed"
			if result == "no_change" {
				kind = "no_change"
			}
		}
		s.activity(id, kind, now)
	}
	if err := s.save(); err != nil {
		return err
	}
	_, statusErr := s.readStatus(b)
	if !s.data.MuteIssues && s.notify != nil && (conflictAdded || code != old && (code == "delete_confirmation" || code == "auth_required")) {
		s.notify(id)
	}
	if code != "" {
		return errors.New(code)
	}
	if code == "" && result == "ok" && s.data.NotifyDone && s.notify != nil {
		s.notify("")
	}
	return statusErr
}
func (s *Workspace) activity(id, kind string, t time.Time) {
	s.data.Activity = append(s.data.Activity, Activity{t.UTC().Format(time.RFC3339Nano), id, kind})
	if len(s.data.Activity) > 200 {
		s.data.Activity = s.data.Activity[len(s.data.Activity)-200:]
	}
}
func (s *Workspace) tick(ctx context.Context, now time.Time) {
	if !s.mu.TryLock() {
		return
	}
	defer s.mu.Unlock()
	s.updateWatches(ctx)
	var next string
	for id, w := range s.watches {
		if s.due[id].After(now) && (s.problems[id] != "" || !w.changed(now, s.states[id].SyncBase)) {
			continue
		}
		if next == "" || s.due[id].Before(s.due[next]) {
			next = id
		}
	}
	if next != "" {
		_ = s.syncOne(next, "")
	}
}

type Commit struct {
	CommitId, CreatedAt, Message, DeviceId string
	Parents                                []string
}
type HistoryPage struct {
	Commits       []Commit
	NextPageToken string
}

func (s *Workspace) History(id, cursor string) (HistoryPage, error) {
	p := s.readSnapshot()
	if s.closed.Load() || p.clientDir == "" {
		return HistoryPage{}, errBinding
	}
	for _, b := range p.view.Libraries {
		if b.ID == id && b.Phase == "bound" {
			var page HistoryPage
			err := s.query(&page, "", "library", "history", "list", "--client-dir", p.clientDir, "--worktree", b.Worktree, "--output=json", "--page-size", "30", "--page-token", cursor)
			return page, err
		}
	}
	return HistoryPage{}, errBinding
}
