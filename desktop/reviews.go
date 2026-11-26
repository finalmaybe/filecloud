package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

func (s *Workspace) CheckServer(server string) error {
	return s.query(nil, "", "check-server", "--server", strings.TrimSpace(server))
}

type BindingCheck struct {
	LocalItems       int
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
	v, err := readPreview(f, name, info.Size())
	v.Size = info.Size()
	v.ModifiedAt = info.ModTime().UTC().Format(time.RFC3339)
	return v, err
}

// Resolve the actual target before handing it to a native application.
func (s *Workspace) localTarget(id, name string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.bound[id]
	if s.locked() || b.Phase != "bound" {
		return "", errBinding
	}
	local, err := filepath.Localize(name)
	if err != nil {
		return "", errors.New("无效的文件位置。")
	}
	file, err := filepath.EvalSymlinks(filepath.Join(b.Worktree, local))
	if err != nil {
		return "", errors.New("此项目在本机不可用，请先同步或检查本地文件夹。")
	}
	rel, err := filepath.Rel(b.Worktree, file)
	if err != nil || !filepath.IsLocal(rel) {
		return "", errors.New("此项目不在工作目录内。")
	}
	return file, nil
}
func (s *Workspace) RevealLocal(id, name string) error {
	file, err := s.localTarget(id, name)
	if err != nil {
		return err
	}
	return s.reveal(file)
}
func (s *Workspace) OpenLocalDocument(id, name string) error {
	file, err := s.localTarget(id, name)
	if err != nil {
		return err
	}
	info, err := os.Stat(file)
	if err != nil {
		return err
	}
	switch strings.ToLower(filepath.Ext(file)) {
	case ".pdf", ".png", ".jpg", ".jpeg", ".gif", ".webp", ".txt", ".md", ".csv", ".json", ".log", ".yaml", ".yml", ".toml", ".ini", ".xml":
		if info.Mode().IsRegular() && info.Mode().Perm()&0111 == 0 {
			return s.open(file)
		}
	}
	return errors.New("此类型请使用快速预览，或在文件管理器中显示。")
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
