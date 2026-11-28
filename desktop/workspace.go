package main

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zalando/go-keyring"
)

const secretService = "org.filecloud.desktop"

var errSession = errors.New("请登录当前账号后再试。")
var errBinding = errors.New("找不到当前资料库的本机绑定。")

type account struct {
	Server, UserID string
	LogoutPending  bool
}

func (a account) key() string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(a.Server+"\n"+a.UserID)))
}

type Activity struct{ Time, LibraryID, Kind string }
type choice struct {
	Worktree       string
	Import         bool
	MergeCandidate string `json:",omitempty"`
}
type profile struct {
	Device, Username, DeviceName string
	LastSuccess                  map[string]string
	Conflicts                    map[string][]Conflict
	NotifyDone, MuteIssues       bool
	Names                        map[string]string
	Paused                       map[string]bool
	Intents                      map[string]choice
	Activity                     []Activity
}
type library struct{ LibraryId, DeviceId, Worktree, Phase string }
type status struct {
	LibraryId, Worktree, Pending, SyncBase string
	Confirmation                           *struct{ Kind, CandidatePrefix string }
	Deletion                               *struct{ Deleted, Tracked int64 }
	Restore                                *RestoreImpact
}
type LibraryView struct {
	ID, Name, Worktree, Phase, Pending, Problem string
	Paused, Running, Scanned, CanReselect       bool
	Deleted                                     int64
	DeviceId                                    string
	LastSuccess                                 string
	Conflicts                                   []Conflict
}
type View struct {
	DefaultTrashDays, DefaultHistoryDays                        int
	Revision                                                    int
	Server, Username, DeviceName, Version                       string
	NotifyDone, MuteIssues                                      bool
	SignedIn, CatalogSession, LogoutPending, CatalogFresh, More bool
	Notice                                                      string
	Libraries                                                   []LibraryView
	Activity                                                    []Activity
}
type Review struct {
	ID, Worktree, Candidate string
	Deleted, Tracked        int64
}
type Workspace struct {
	mu                                        sync.Mutex
	view                                      atomic.Pointer[View]
	root, cli, token, notice, cursor, running string
	active                                    account
	data                                      profile
	bound                                     map[string]library
	states                                    map[string]status
	problems                                  map[string]string
	due                                       map[string]time.Time
	delays                                    map[string]time.Duration
	watches                                   map[string]*bindingWatch
	review                                    *Review
	selection                                 choice
	selectionID                               string
	createID, createName                      string
	defaultTrashDays, defaultHistoryDays      int
	fresh, scanDone, catalogExpired           bool
	closed                                    atomic.Bool
	pick                                      func() (string, error)
	restoreReview                             *RestoreImpact
	openWeb                                   func(string) error
	reveal                                    func(string) error
	saveFile                                  func(string) (string, error)
	open                                      func(string) error
	notify                                    func(string)
}

func newWorkspace(root, cli, version, commit string) (*Workspace, error) {
	s := &Workspace{root: root, cli: cli}
	var build struct{ Version, Commit string }
	if err := s.query(&build, "", "version"); err != nil || build.Version != version || build.Commit != commit {
		return nil, errors.New("随包 filecloud-client 缺失或版本不匹配，请重新安装。")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	if err := loadJSON(filepath.Join(root, "account.json"), &s.active); err != nil {
		return nil, err
	}
	if err := s.loadProfile(); err != nil {
		return nil, err
	}
	if s.active.UserID != "" {
		s.token, _ = keyring.Get(secretService, s.active.key())
		if !s.active.LogoutPending {
			if err := s.loadBindings(); err != nil {
				s.notice = err.Error()
			}
		}
	}
	s.publish()
	return s, nil
}
func (s *Workspace) loadProfile() error {
	s.stopWatches()
	s.data = profile{Device: newUUID(), Names: map[string]string{}, Paused: map[string]bool{}, Intents: map[string]choice{}}
	s.bound = map[string]library{}
	s.states = map[string]status{}
	s.problems = map[string]string{}
	s.due = map[string]time.Time{}
	s.delays = map[string]time.Duration{}
	s.review = nil
	s.restoreReview = nil
	s.selectionID = ""
	s.cursor = ""
	s.defaultTrashDays, s.defaultHistoryDays = 0, 0
	s.fresh = false
	s.catalogExpired = false
	s.createID = ""
	s.running = ""
	if s.active.UserID == "" {
		return nil
	}
	if err := os.MkdirAll(s.profileDir(), 0700); err != nil {
		return err
	}
	s.data.LastSuccess = map[string]string{}
	s.data.Conflicts = map[string][]Conflict{}
	return loadJSON(filepath.Join(s.profileDir(), "workspace.json"), &s.data)
}
func (s *Workspace) profileDir() string { return filepath.Join(s.root, s.active.key()) }
func (s *Workspace) clientDir() string  { return filepath.Join(s.profileDir(), "sync") }
func (s *Workspace) save() error {
	return saveJSON(filepath.Join(s.profileDir(), "workspace.json"), &s.data)
}
func loadJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if json.Unmarshal(b, v) != nil {
		return errors.New("桌面配置不可读取，已停止操作。")
	}
	return nil
}
func saveJSON(path string, v any) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".workspace-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	err = json.NewEncoder(f).Encode(v)
	if err == nil {
		err = f.Sync()
	}
	if err = errors.Join(err, f.Close()); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
func newUUID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&15 | 64
	b[8] = b[8]&63 | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
func (s *Workspace) exec(secret string, args ...string) ([]byte, error) {
	cmd := exec.Command(s.cli, args...)
	hideConsole(cmd)
	if secret != "" {
		cmd.Stdin = strings.NewReader(secret + "\n")
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	return out.Bytes(), err
}
func (s *Workspace) query(dst any, secret string, args ...string) error {
	data, err := s.exec(secret, append([]string{"--machine"}, args...)...)
	if err != nil {
		var msg struct{ Code string }
		if json.Unmarshal(data, &msg) == nil && msg.Code != "" {
			return commandError(msg.Code)
		}
		return errors.New("操作未完成，请检查服务连接、登录状态或目录。")
	}
	if dst != nil && json.Unmarshal(data, dst) != nil {
		return errors.New("随包 CLI 返回了无法识别的结果。")
	}
	return nil
}

type commandError string

func (c commandError) Error() string { return string(c) }

func (s *Workspace) locked() bool {
	return s.closed.Load() || s.active.LogoutPending || s.active.UserID == ""
}
func (s *Workspace) publish() {
	v := &View{Version: version, Username: s.data.Username, DeviceName: s.data.DeviceName, NotifyDone: s.data.NotifyDone, MuteIssues: s.data.MuteIssues, DefaultTrashDays: s.defaultTrashDays, DefaultHistoryDays: s.defaultHistoryDays, Server: s.active.Server, SignedIn: s.active.UserID != "" && !s.active.LogoutPending, CatalogSession: s.token != "" && !s.catalogExpired, LogoutPending: s.active.LogoutPending, CatalogFresh: s.fresh, More: s.cursor != "", Notice: s.notice, Libraries: []LibraryView{}, Activity: []Activity{}}
	if prev := s.view.Load(); prev != nil {
		v.Revision = prev.Revision + 1
	}
	if v.SignedIn {
		v.Activity = slices.Clone(s.data.Activity)
		ids := map[string]bool{}
		for id := range s.data.Names {
			ids[id] = true
		}
		for id := range s.bound {
			ids[id] = true
		}
		for id := range s.data.Intents {
			ids[id] = true
		}
		for id := range ids {
			b := s.bound[id]
			name := s.data.Names[id]
			if name == "" {
				name = id
			}
			if c, ok := s.data.Intents[id]; ok && b.Phase == "" {
				b.Phase = "binding"
				b.Worktree = c.Worktree
			}
			var deleted int64
			if d := s.states[id].Deletion; d != nil {
				deleted = d.Deleted
			}
			v.Libraries = append(v.Libraries, LibraryView{CanReselect: s.bound[id].Phase == "", Scanned: s.running == id && s.scanDone, Deleted: deleted, DeviceId: b.DeviceId, ID: id, Name: name, Worktree: b.Worktree, Phase: b.Phase, Pending: s.states[id].Pending, Problem: s.problems[id], Paused: s.data.Paused[id], Running: s.running == id, LastSuccess: s.data.LastSuccess[id], Conflicts: slices.Clone(s.data.Conflicts[id])})
		}
		sort.Slice(v.Libraries, func(i, j int) bool {
			a, b := v.Libraries[i], v.Libraries[j]
			if a.Name == b.Name {
				return a.ID < b.ID
			}
			return a.Name < b.Name
		})
	}
	s.view.Store(v)
}
func (s *Workspace) Snapshot() *View { return s.view.Load() }
func (s *Workspace) loadBindings() error {
	var list []library
	if err := s.query(&list, "", "library", "bindings", "--client-dir", s.clientDir(), "--server", s.active.Server, "--user-id", s.active.UserID); err != nil {
		return err
	}
	s.bound = map[string]library{}
	for _, b := range list {
		s.bound[b.LibraryId] = b
		if b.Phase == "bound" {
			s.readStatus(b)
		}
	}
	return nil
}
func (s *Workspace) readStatus(b library) (status, error) {
	var st status
	err := s.query(&st, "", "library", "status", "--client-dir", s.clientDir(), "--worktree", b.Worktree)
	if err != nil {
		s.problems[b.LibraryId] = "status_unavailable"
		return st, err
	}
	if s.problems[b.LibraryId] == "status_unavailable" || (s.problems[b.LibraryId] == "delete_confirmation" && st.Pending != "delete-confirmation") {
		delete(s.problems, b.LibraryId)
	}
	s.states[b.LibraryId] = st
	return st, nil
}
func (s *Workspace) Login(server, username, password, device string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.publish()
	if s.closed.Load() || s.active.LogoutPending {
		return errors.New("请先完成退出账号。")
	}
	server = strings.ToLower(strings.TrimRight(strings.TrimSpace(server), "/"))
	if s.active.UserID != "" && server != s.active.Server {
		return errors.New("更换服务前请先退出当前账号。")
	}
	var r struct {
		Session struct{ UserId, AccessToken string }
	}
	if err := s.query(&r, password, "login", "--server", server, "--username", username, "--device-name", device, "--password-stdin"); err != nil {
		return err
	}
	if r.Session.UserId == "" || r.Session.AccessToken == "" {
		return errSession
	}
	if s.active.UserID != "" && s.active.UserID != r.Session.UserId {
		_ = s.query(nil, r.Session.AccessToken, "logout", "--server", server, "--token-stdin")
		return errors.New("更换账号前请先退出当前账号。")
	}
	s.active = account{Server: server, UserID: r.Session.UserId}
	if err := s.loadProfile(); err != nil {
		return err
	}
	if err := saveJSON(filepath.Join(s.root, "account.json"), s.active); err != nil {
		return err
	}
	s.data.Username, s.data.DeviceName = username, device
	s.token = r.Session.AccessToken
	s.notice = ""
	if err := keyring.Set(secretService, s.active.key(), s.token); err != nil {
		s.notice = "系统凭据库不可用，本次会话仅保存在内存中。"
	}
	if err := s.save(); err != nil {
		return err
	}
	if err := s.loadBindings(); err != nil {
		return err
	}
	return s.catalog(false)
}
func (s *Workspace) Logout() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.publish()
	if s.active.UserID == "" {
		return nil
	}
	s.active.LogoutPending = true
	if err := saveJSON(filepath.Join(s.root, "account.json"), s.active); err != nil {
		return err
	}
	if s.token == "" {
		return errors.New("本机已停止同步，原会话不可用，无法核实远端撤销。")
	}
	if err := s.query(nil, s.token, "logout", "--server", s.active.Server, "--token-stdin"); err != nil && !errors.Is(err, commandError("auth_required")) {
		return errors.New("本机已停止同步，远端注销尚未完成。")
	}
	return s.clearAccount()
}
func (s *Workspace) ForgetUnavailableSession() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.publish()
	if !s.active.LogoutPending {
		return errSession
	}
	return s.clearAccount()
}
func (s *Workspace) clearAccount() error {
	if err := saveJSON(filepath.Join(s.root, "account.json"), account{}); err != nil {
		return err
	}
	_ = keyring.Delete(secretService, s.active.key())
	s.token = ""
	s.active = account{}
	s.notice = ""
	return s.loadProfile()
}
func (s *Workspace) Refresh(more bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.publish()
	if s.locked() {
		return errSession
	}
	if err := s.loadBindings(); err != nil {
		return err
	}
	return s.catalog(more)
}
func (s *Workspace) RetentionDefaults() (Policy, error) {
	s.mu.Lock()
	server, token := s.active.Server, s.token
	allowed := !s.locked() && !s.catalogExpired && token != ""
	s.mu.Unlock()
	if !allowed {
		return Policy{}, errSession
	}
	// Settings only need the defaults, not a refresh of the local catalog.
	var r struct{ DefaultRetention Policy }
	err := s.query(&r, token, "library", "list", "--server", server, "--page-size", "1", "--token-stdin")
	return r.DefaultRetention, err
}
func (s *Workspace) catalog(more bool) error {
	if s.token == "" {
		return errSession
	}
	args := []string{"library", "list", "--server", s.active.Server, "--token-stdin"}
	if more {
		if s.cursor == "" {
			return nil
		}
		args = append(args, "--page-token", s.cursor)
	}
	var r struct {
		DefaultRetention struct{ TrashDays, HistoryDays int }
		Libraries        []struct{ LibraryId, Name string }
		NextPageToken    string
	}
	if err := s.query(&r, s.token, args...); err != nil {
		if errors.Is(err, commandError("auth_required")) {
			s.catalogExpired = true
		}
		s.fresh = false
		return err
	}
	if !more {
		for id := range s.data.Names {
			if _, bound := s.bound[id]; !bound {
				delete(s.data.Names, id)
			}
		}
	}
	for _, l := range r.Libraries {
		s.data.Names[l.LibraryId] = l.Name
	}
	s.defaultTrashDays, s.defaultHistoryDays = r.DefaultRetention.TrashDays, r.DefaultRetention.HistoryDays
	s.cursor = r.NextPageToken
	s.fresh = true
	s.catalogExpired = false
	return s.save()
}
func (s *Workspace) CreateLibrary(name string, trashDays, historyDays int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	defer s.publish()
	if s.locked() || s.token == "" {
		return errSession
	}
	if s.createID == "" || name != s.createName {
		s.createID = newUUID()
		s.createName = name
	}
	args := []string{"library", "create", "--server", s.active.Server, "--library-id", s.createID, "--name", name, "--token-stdin"}
	if trashDays != 0 {
		args = append(args, "--trash-days", fmt.Sprint(trashDays))
	}
	if historyDays != 0 {
		args = append(args, "--history-days", fmt.Sprint(historyDays))
	}
	if err := s.query(nil, s.token, args...); err != nil {
		return err
	}
	s.data.Names[s.createID] = name
	s.createID = ""
	return s.save()
}
