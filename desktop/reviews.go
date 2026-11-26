package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
)

type BindingCheck struct {
	Path, Filesystem string
	Empty, Published bool
}

func (s *Workspace) CheckDirectory(id string) (BindingCheck, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.locked() || s.token == "" || s.selectionID != id || s.selection.Worktree == "" {
		return BindingCheck{}, errBinding
	}
	v := BindingCheck{Path: s.selection.Worktree}
	if err := s.query(&v, "", "library", "check-directory", "--worktree", v.Path); err != nil {
		return v, err
	}
	var info struct{ Library LibraryInfo }
	if err := s.query(&info, s.token, "library", "inspect", "--server", s.active.Server, "--library-id", id, "--token-stdin"); err != nil {
		return v, err
	}
	v.Published = info.Library.HeadCommitId != ""
	return v, nil
}

type RestoreImpact struct {
	ID, Worktree, SourceCommit, SourcePath, Candidate string
	Created, Updated, Replaced, Removed, Preserved    int64
}

func (s *Workspace) PreviewRestore(id, commit, path string) (*RestoreImpact, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.publish()
	b := s.bound[id]
	if s.locked() || b.Phase != "bound" {
		return nil, errBinding
	}
	s.restoreReview = nil
	if commit != "" {
		if err := s.query(nil, "", "library", "restore", "--client-dir", s.clientDir(), "--worktree", b.Worktree, "--commit", commit, "--path", path); err != nil {
			return nil, err
		}
	}
	st, err := s.readStatus(b)
	if err != nil || st.Restore == nil {
		return nil, err
	}
	impact := *st.Restore
	impact.ID, impact.Worktree = id, b.Worktree
	s.restoreReview = &impact
	return &impact, nil
}
func (s *Workspace) FinishRestore(v RestoreImpact, confirm bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.publish()
	if s.locked() || s.restoreReview == nil || *s.restoreReview != v || s.bound[v.ID].Worktree != v.Worktree {
		return commandError("stale_candidate")
	}
	s.restoreReview = nil
	flag := "--cancel"
	if confirm {
		flag = "--confirm"
	}
	err := s.query(nil, "", "library", "restore", "--client-dir", s.clientDir(), "--worktree", v.Worktree, flag, v.Candidate)
	_, statusErr := s.readStatus(s.bound[v.ID])
	if err != nil {
		return err
	}
	return statusErr
}

type Conflict struct{ Source, Target string }

func (s *Workspace) ConflictPreview(id string, pair Conflict, copySide bool) (Preview, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.bound[id]
	if s.locked() || b.Phase != "bound" || !slices.Contains(s.data.Conflicts[id], pair) {
		return Preview{}, errBinding
	}
	name := pair.Source
	if copySide {
		name = pair.Target
	}
	return localPreview(b.Worktree, name)
}

func (s *Workspace) LocalPreview(id, name string) (Preview, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.bound[id]
	if s.locked() || b.Phase != "bound" {
		return Preview{}, errBinding
	}
	return localPreview(b.Worktree, name)
}

func localPreview(worktree, name string) (Preview, error) {
	local, err := filepath.Localize(name)
	if err != nil {
		return Preview{}, err
	}
	root, err := os.OpenRoot(worktree)
	if err != nil {
		return Preview{}, err
	}
	defer root.Close()
	info, err := root.Lstat(local)
	if errors.Is(err, fs.ErrNotExist) {
		return Preview{Notice: "此路径已移动或不存在。"}, nil
	}
	if err != nil {
		return Preview{}, err
	}
	if !info.Mode().IsRegular() {
		return Preview{Notice: "请打开本地文件夹查看此项目。"}, nil
	}
	f, err := root.Open(local)
	if err != nil {
		return Preview{}, err
	}
	defer f.Close()
	return readPreview(f, name, info.Size())
}
func (s *Workspace) DismissConflict(id string, pair Conflict) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.locked() {
		return errSession
	}
	s.data.Conflicts[id] = slices.DeleteFunc(s.data.Conflicts[id], func(p Conflict) bool { return p == pair })
	s.publish()
	return s.save()
}
func (s *Workspace) NotificationPolicy(issues, done bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.locked() {
		return errSession
	}
	s.data.MuteIssues, s.data.NotifyDone = !issues, done
	s.publish()
	return s.save()
}
func (s *Workspace) PauseAll(on bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.locked() {
		return errSession
	}
	for id, b := range s.bound {
		if b.Phase == "bound" {
			s.data.Paused[id] = on
			delete(s.due, id)
		}
	}
	s.publish()
	return s.save()
}
