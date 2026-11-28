package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mingming-cn/filecloud/internal/officepreview"
)

type FileEntry struct{ Name, Type, ModifiedAt, Size string }
type Directory struct {
	CommitId, Path, Parent string
	Entries                []FileEntry
	NextOffset             int
}
type FileVersion struct {
	CommitId, CreatedAt, ModifiedAt, Change string
	IsCurrent                               bool
}
type VersionsPage struct {
	Versions      []FileVersion
	NextPageToken string
}
type TrashEntry struct {
	ID                               int64
	Path, Kind, DeletedAt, ExpiresAt string
}
type TrashPage struct {
	Entries []TrashEntry
	Next    int64
}
type Policy struct{ TrashDays, HistoryDays int }
type PolicyImpact struct {
	Count, RestoredRecords int
	Bytes                  int64
	Token                  string
}
type LibraryInfo struct {
	HeadCommitId string
	Policy
}
type Preview struct {
	Media, Content, Notice, ModifiedAt string
	Size                               int64
	Sheets                             []officepreview.Sheet
}
type LibraryStats struct {
	Size, Items string
	Published   bool
}

var desktopHTTP = &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

// Only named library operations reach this bridge. Credentials never enter the webview or browser URL.
func (s *Workspace) catalogRequest(id, endpoint string, q url.Values, body any) (*http.Response, error) {
	p := s.catalogAccess(body != nil)
	_, known := p.known[id]
	if !known || p.token == "" || s.closed.Load() {
		return nil, errSession
	}
	target, err := url.Parse(p.view.Server)
	if err != nil {
		return nil, errSession
	}
	target = target.JoinPath("v1/libraries", id, endpoint)
	target.RawQuery = q.Encode()
	var buf bytes.Buffer
	method := "GET"
	if body != nil {
		method = "POST"
		if err = json.NewEncoder(&buf).Encode(body); err != nil {
			return nil, err
		}
	}
	req, err := http.NewRequest(method, target.String(), &buf)
	if err != nil {
		return nil, errSession
	}
	req.Header.Set("Authorization", "Bearer "+p.token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := desktopHTTP.Do(req)
	if err != nil {
		return nil, commandError("offline")
	}
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		return res, nil
	}
	defer res.Body.Close()
	if res.StatusCode == 503 {
		var failure struct{ Reason string }
		_ = json.NewDecoder(io.LimitReader(res.Body, 4096)).Decode(&failure)
		if failure.Reason == "DAVLocked" {
			return nil, commandError("dav_locked")
		}
	}
	switch res.StatusCode {
	case 401:
		s.setCatalogAvailable(p.token, false)
		return nil, commandError("auth_required")
	case 403:
		return nil, errors.New("此账号无权访问所选资料库。")
	case 404, 405:
		if endpoint == "restore-location" {
			return nil, commandError("check_unavailable")
		}
		if res.StatusCode == 405 || !strings.Contains(res.Header.Get("Content-Type"), "application/json") {
			return nil, errors.New("当前服务端未提供此接口。请更新 filecloud-server 并重启服务后重试。")
		}
		if endpoint == "trash/delete" {
			return nil, errors.New("找不到所选资料库或部分回收站条目；本次未删除任何条目，请重新打开回收站。")
		}
		return nil, errors.New("找不到所选资料库、路径或版本，请刷新资料库后重试。")
	case 409:
		return nil, errors.New("恢复位置已有同名项目，请改名或取消。原有内容已保留。")
	case 410:
		return nil, errors.New("此快照已到期。")
	case 412:
		return nil, commandError("stale_candidate")
	case 429, 503:
		return nil, commandError("retry_later")
	}
	return nil, errors.New("请求未完成，请检查所选路径和版本。")
}

// Mutations still wait for the current workspace operation; browsing uses the
// credentials and allowed library IDs published with the last view.
func (s *Workspace) catalogAccess(mutation bool) workspaceSnapshot {
	if mutation {
		s.mu.Lock()
		defer s.mu.Unlock()
	}
	return s.readSnapshot()
}

func (s *Workspace) catalogJSON(id, endpoint string, q url.Values, body, dst any) error {
	res, err := s.catalogRequest(id, endpoint, q, body)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if dst == nil {
		return nil
	}
	if json.NewDecoder(res.Body).Decode(dst) != nil {
		return errors.New("服务端返回了无法识别的数据。")
	}
	return nil
}
func (s *Workspace) Files(id, commit, dir string, offset int) (Directory, error) {
	q := url.Values{"Path": {dir}, "Offset": {strconv.Itoa(offset)}}
	if commit != "" {
		q.Set("CommitId", commit)
	}
	var v struct{ Directory Directory }
	err := s.catalogJSON(id, "files", q, nil, &v)
	return v.Directory, err
}
func (s *Workspace) LibraryInfo(id string) (LibraryInfo, error) {
	var v struct{ Library LibraryInfo }
	err := s.catalogJSON(id, "", nil, nil, &v)
	return v.Library, err
}
func (s *Workspace) LibraryStats(id string) (LibraryStats, error) {
	info, err := s.LibraryInfo(id)
	if err != nil {
		return LibraryStats{}, err
	}
	if info.HeadCommitId == "" {
		return LibraryStats{Size: "0", Items: "0"}, nil
	}
	v := LibraryStats{Published: true}
	err = s.catalogJSON(id, "size", url.Values{"CommitId": {info.HeadCommitId}}, nil, &v)
	return v, err
}
func (s *Workspace) CheckRestoreLocation(id, destination string) (bool, error) {
	var v struct{ Available bool }
	err := s.catalogJSON(id, "restore-location", url.Values{"Path": {destination}}, nil, &v)
	return v.Available, err
}

func (s *Workspace) FileVersions(id, commit, file, cursor string) (VersionsPage, error) {
	q := url.Values{"CommitId": {commit}, "Path": {file}}
	if cursor != "" {
		q.Set("PageToken", cursor)
	}
	var v struct{ FileHistory VersionsPage }
	err := s.catalogJSON(id, "file-history", q, nil, &v)
	return v.FileHistory, err
}
func (s *Workspace) RestoreVersion(id, commit, file, anchor string) (string, error) {
	if !validCommit(commit) {
		return "", errors.New("无效的版本。")
	}
	var v struct{ Head string }
	err := s.catalogJSON(id, "file-history/"+commit+"/restore", url.Values{"AnchorCommitId": {anchor}, "Path": {file}}, struct{}{}, &v)
	return v.Head, err
}
func validCommit(id string) bool { return len(id) == 64 && strings.Trim(id, "0123456789abcdef") == "" }
func (s *Workspace) Trash(id string, before int64) (TrashPage, error) {
	var v TrashPage
	q := url.Values{}
	if before > 0 {
		q.Set("Before", strconv.FormatInt(before, 10))
	}
	err := s.catalogJSON(id, "trash", q, nil, &v)
	return v, err
}
func (s *Workspace) RestoreTrash(id string, item int64, dest string) error {
	return s.catalogJSON(id, fmt.Sprintf("trash/%d/restore", item), url.Values{"Destination": {dest}}, struct{}{}, nil)
}
func (s *Workspace) DeleteTrash(id string, items []int64) error {
	return s.catalogJSON(id, "trash/delete", nil, struct{ IDs []int64 }{items}, nil)
}
func (s *Workspace) Retention(id string, p Policy, apply bool, confirm string) (PolicyImpact, error) {
	var v PolicyImpact
	err := s.catalogJSON(id, "retention", nil, struct {
		Policy
		Apply   bool
		Confirm string
	}{p, apply, confirm}, &v)
	return v, err
}
func (s *Workspace) MergeSource(id, commit string) (Commit, error) {
	if !validCommit(commit) {
		return Commit{}, errors.New("无效的合并来源。")
	}
	var v struct{ HistoryCommit Commit }
	err := s.catalogJSON(id, "history/"+commit, nil, nil, &v)
	return v.HistoryCommit, err
}
func (s *Workspace) AppPasswords(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.data.Names[id]; s.locked() || !ok {
		return errSession
	}
	base, err := url.Parse(s.active.Server)
	if err != nil {
		return errSession
	}
	return s.openWeb(base.JoinPath("libraries", id, "app-passwords").String())
}
func (s *Workspace) PreviewFile(id, commit, file string) (Preview, error) {
	res, err := s.catalogRequest(id, "content", url.Values{"Path": {file}, "CommitId": {commit}}, nil)
	if err != nil {
		return Preview{}, err
	}
	defer res.Body.Close()
	return readPreview(res.Body, file, res.ContentLength)
}
func readPreview(r io.Reader, name string, size int64) (Preview, error) {
	const textLimit = 256 << 10
	if media := officepreview.Media(name); media != "" {
		doc, err := officepreview.Read(r, media, size)
		if err != nil {
			return Preview{}, errors.New("读取文件失败。")
		}
		if doc.Text == "" && len(doc.Sheets) == 0 {
			return Preview{Notice: doc.Notice}, nil
		}
		return Preview{Media: media, Content: doc.Text, Sheets: doc.Sheets, Notice: doc.Notice}, nil
	}
	ext := strings.ToLower(path.Ext(name))
	media := map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp", ".pdf": "application/pdf"}[ext]
	limit := textLimit
	if media != "" {
		limit = 12 << 20
		if size > int64(limit) {
			return Preview{Notice: "文件较大，请下载后查看。"}, nil
		}
	}
	data, err := io.ReadAll(io.LimitReader(r, int64(limit+1)))
	if err != nil {
		return Preview{}, errors.New("读取文件失败。")
	}
	if media != "" {
		if len(data) > limit || http.DetectContentType(data) != media {
			return Preview{Notice: "此文件不支持快速预览。"}, nil
		}
		return Preview{Media: media, Content: base64.StdEncoding.EncodeToString(data)}, nil
	}
	notice := ""
	if len(data) > limit {
		data = data[:limit]
		for i := len(data) - 1; i >= 0 && i >= limit-utf8.UTFMax; i-- {
			if utf8.RuneStart(data[i]) {
				if !utf8.FullRune(data[i:]) {
					data = data[:i]
				}
				break
			}
		}
		notice = "仅显示前 256 KiB。"
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return Preview{Notice: "此类型不支持快速预览，可下载原文件。"}, nil
	}
	return Preview{Media: "text/plain", Content: string(data), Notice: notice}, nil
}
func (s *Workspace) DownloadFile(id, commit, file string) error {
	dest, err := s.saveFile(filepath.Base(filepath.FromSlash(file)))
	if err != nil || dest == "" {
		return err
	}
	res, err := s.catalogRequest(id, "content", url.Values{"CommitId": {commit}, "Path": {file}}, nil)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".filecloud-download-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, err = io.Copy(tmp, res.Body)
	if err = errors.Join(err, tmp.Close()); err != nil {
		return errors.New("下载未完成，原文件已保留。")
	}
	return os.Rename(tmp.Name(), dest)
}
