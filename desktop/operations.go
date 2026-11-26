package main

import (
	"bytes"
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
func (s *Workspace) Bind(id, path string, allowImport bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.publish()
	if s.locked() || s.token == "" {
		return errSession
	}
	if id != s.selectionID || path != s.selection.Worktree || path == "" {
		return errors.New("请重新选择工作目录。")
	}
	s.selectionID = ""
	s.data.Intents[id] = choice{path, allowImport}
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
func (s *Workspace) bind(id string, c choice, device string) error {
	args := []string{"library", "bind", "--server", s.active.Server, "--library-id", id, "--device-id", device, "--client-dir", s.clientDir(), "--worktree", c.Worktree, "--token-stdin"}
	if c.Import {
		args = append(args, "--import-local")
	}
	if err := s.query(nil, s.token, args...); err != nil {
		_ = s.loadBindings()
		if code, ok := err.(commandError); ok && code != "unknown" {
			return err
		}
		return errors.New("绑定未完成。请检查目录和凭据；已有远端版本请选空目录。刷新凭据要求两端与同步基线一致。")
	}
	delete(s.data.Intents, id)
	delete(s.problems, id)
	delete(s.due, id)
	if err := s.loadBindings(); err != nil {
		return err
	}
	return s.save()
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
		return errors.New("解绑未完成；有待办时请先处理。")
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
		return errors.New("请重新核对删除影响。")
	}
	current, err := s.reviewDeletion(v.ID)
	s.review = nil
	if err != nil || *current != v {
		return errors.New("变更已更新，请重新核对。")
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
	if prefix == "" && (st.Pending == "delete-confirmation" || st.Pending == "restore-confirmation") {
		s.publish()
		return errors.New("此资料库需要先核对待办。")
	}
	s.running = id
	s.publish()
	defer func() { s.running = ""; s.publish() }()
	args := []string{"library", "sync", "--output=jsonl", "--conflict-paths", "--client-dir", s.clientDir(), "--worktree", b.Worktree}
	if prefix != "" {
		args = append(args, "--confirm-delete", prefix)
	}
	data, runErr := s.exec("", args...)
	dec := json.NewDecoder(bytes.NewReader(data))
	var result, code string
	var scanned, conflictAdded bool
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
		case "result":
			result, code = msg.Outcome, msg.Code
		}
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
	delay := 30 * time.Second
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
func (s *Workspace) tick(now time.Time) {
	if !s.mu.TryLock() {
		return
	}
	defer s.mu.Unlock()
	if s.locked() {
		return
	}
	var next string
	for id, b := range s.bound {
		pending := s.states[id].Pending
		code := s.problems[id]
		if b.Phase != "bound" || s.data.Paused[id] || pending == "delete-confirmation" || pending == "restore-confirmation" || code == "auth_required" || code == "status_unavailable" || code == "unsupported_fs" || s.due[id].After(now) {
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
	CommitId, CreatedAt, Message string
	Parents                      []string
}
type HistoryPage struct {
	Commits       []Commit
	NextPageToken string
}

func (s *Workspace) History(id, cursor string) (HistoryPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.bound[id]
	if s.locked() || b.Phase != "bound" {
		return HistoryPage{}, errBinding
	}
	var page HistoryPage
	err := s.query(&page, "", "library", "history", "list", "--client-dir", s.clientDir(), "--worktree", b.Worktree, "--output=json", "--page-size", "30", "--page-token", cursor)
	return page, err
}
