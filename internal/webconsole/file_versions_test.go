package webconsole_test

import (
	"bytes"
	"fmt"
	"mime"
	"net/url"
	"strings"
	"testing"

	"github.com/mingming-cn/filecloud/internal/auth"
	"github.com/mingming-cn/filecloud/internal/object"
)

func TestFileVersionActions(t *testing.T) {
	f := newFixture(t)
	f.login()
	ctx := t.Context()
	user, err := f.db.FindUserByUsername(ctx, "observer")
	if err != nil {
		t.Fatal(err)
	}
	lib, _ := auth.NewUserID(nil)
	if w := f.libraryAPI("PUT", "/"+lib, `{"Name":"Versions"}`); w.Code != 201 {
		t.Fatal(w.Code)
	}
	put := func(kind string, data []byte) string {
		t.Helper()
		id := object.ID(data)
		if _, err := f.db.PutObject(ctx, user.ID, lib, kind, id, bytes.NewReader(data)); err != nil {
			t.Fatal(err)
		}
		return id
	}
	oldBytes := bytes.Repeat([]byte{'A'}, object.MaxBlockSize)
	block := put("blocks", oldBytes)
	tail := put("blocks", []byte{0, 1, 255})
	oldBytes = append(oldBytes, 0, 1, 255)
	oldFile := put("files", []byte(fmt.Sprintf(`{"Blocks":[%q,%q],"Size":"%d","Type":"File","Version":1}`, block, tail, len(oldBytes))))
	empty := put("files", []byte(`{"Blocks":[],"Size":"0","Type":"File","Version":1}`))
	const name = "笔记 + &.bin"
	const original = "2026-08-09T00:00:01Z"
	const updated = "2026-08-09T00:00:02Z"
	makeCommit := func(file, sibling, stamp string, parents []string) string {
		t.Helper()
		sub := put("directories", object.Directory{Entries: []object.DirectoryEntry{{Name: name, Type: "File", ID: file, ModifiedAt: stamp}}}.Bytes())
		root := put("directories", object.Directory{Entries: []object.DirectoryEntry{
			{Name: "dir", Type: "Directory", ID: sub, ModifiedAt: stamp},
			{Name: "sibling", Type: "File", ID: sibling, ModifiedAt: stamp},
		}}.Bytes())
		return put("commits", object.Commit{AuthorUserID: user.ID, DeviceID: lib, Parents: parents, Root: root, CreatedAt: stamp}.Bytes())
	}
	first := makeCommit(oldFile, empty, original, nil)
	head := makeCommit(empty, oldFile, updated, []string{first})
	if _, err := f.db.UpdateLibraryHead(ctx, user.ID, lib, nil, 0, first, nil, f.now); err != nil {
		t.Fatal(err)
	}
	if _, err := f.db.UpdateLibraryHead(ctx, user.ID, lib, &first, 1, head, nil, f.now); err != nil {
		t.Fatal(err)
	}
	base := "/web/libraries/" + lib + "/file-history"
	form := url.Values{"path": {"dir/" + name}, "commit": {first}, "anchor": {head}}
	download := func() string { return base + "/download?" + form.Encode() }
	apiVersion := func() string {
		q := url.Values{"Path": {form.Get("path")}, "AnchorCommitId": {form.Get("anchor")}}
		return "/" + lib + "/file-history/" + form.Get("commit") + "?" + q.Encode()
	}
	t.Run("other owner with same library ID", func(t *testing.T) {
		ownerCookie := f.cookie
		defer func() { f.cookie = ownerCookie }()
		if err := f.sessions.SetAccountPassword(ctx, "second", f.pass, "test", true); err != nil {
			t.Fatal(err)
		}
		login := f.req("POST", "/web/login", url.Values{"username": {"second"}, "password": {f.pass}})
		f.cookie = login.Result().Cookies()[0]
		if w := f.libraryAPI("PUT", "/"+lib, `{"Name":"Other"}`); w.Code != 201 {
			t.Fatal(w.Code)
		}
		if w := f.req("GET", download(), nil); w.Code != 404 {
			t.Fatalf("foreign download: %d", w.Code)
		}
		if w := f.libraryAPI("DELETE", apiVersion(), ""); w.Code != 404 {
			t.Fatalf("foreign API delete: %d", w.Code)
		}
		for _, action := range []string{"restore", "delete"} {
			if w := f.req("POST", base+"/"+action, form); w.Code != 404 {
				t.Fatalf("foreign %s: %d", action, w.Code)
			}
		}
	})
	w := f.req("GET", download(), nil)
	mediaType, params, err := mime.ParseMediaType(w.Header().Get("Content-Disposition"))
	if err != nil || mediaType != "attachment" || params["filename"] != name || w.Code != 200 || !bytes.Equal(w.Body.Bytes(), oldBytes) {
		t.Fatal("historical bytes or attachment filename incorrect")
	}
	for _, action := range []string{"restore", "delete"} {
		if w := f.req("GET", base+"/"+action, nil); w.Code == 200 {
			t.Fatalf("GET %s succeeded", action)
		}
		if w := f.req("POST", base+"/"+action, nil); w.Code != 403 {
			t.Fatalf("missing Origin on %s: %d", action, w.Code)
		}
	}
	form.Set("commit", head)
	if w := f.libraryAPI("DELETE", apiVersion(), ""); w.Code != 409 || !strings.Contains(w.Body.String(), `"RetCode":3007`) {
		t.Fatalf("delete current: %d", w.Code)
	}
	if w := f.req("GET", download(), nil); w.Code != 200 || w.Body.Len() != 0 || w.Header().Get("Content-Length") != "0" {
		t.Fatal("empty download")
	}
	unpublished := makeCommit(oldFile, oldFile, original, []string{head})
	form.Set("commit", unpublished)
	if w := f.req("POST", base+"/restore", form); w.Code != 404 {
		t.Fatalf("restore unpublished: %d", w.Code)
	}
	form.Set("commit", first)
	form.Set("path", "dir")
	if w := f.req("GET", download(), nil); w.Code != 404 {
		t.Fatalf("download directory: %d", w.Code)
	}
	form.Set("path", "dir/"+name)
	form.Set("anchor", first)
	if w := f.req("POST", base+"/restore", form); w.Code != 409 {
		t.Fatalf("stale restore: %d", w.Code)
	}
	form.Set("anchor", head)
	w = f.req("POST", base+"/restore", form)
	if w.Code != 303 {
		t.Fatalf("restore: %d", w.Code)
	}
	restoredPage := f.req("GET", w.Header().Get("Location"), nil)
	if restoredPage.Code != 200 || !strings.Contains(restoredPage.Body.String(), "已将所选版本恢复") {
		t.Fatal("restore result page")
	}
	item, err := f.db.GetLibrary(ctx, user.ID, lib)
	if err != nil || item.HeadVersion != 3 || item.HeadCommitID == nil {
		t.Fatal("restore did not publish")
	}
	restored := *item.HeadCommitID
	if restored == first || restored == head {
		t.Fatal("restore rewound Head")
	}
	snap, err := f.config.Catalog.Snapshot(ctx, user.ID, lib, restored, form.Get("path"), 0)
	if err != nil || snap.ID != oldFile || snap.ModifiedAt != original {
		t.Fatal("restored file or mtime incorrect")
	}
	sibling, err := f.config.Catalog.Snapshot(ctx, user.ID, lib, restored, "sibling", 0)
	if err != nil || sibling.ID != oldFile || sibling.ModifiedAt != updated {
		t.Fatal("restore overwrote sibling")
	}
	if w := f.libraryAPI("DELETE", apiVersion(), ""); w.Code != 412 || !strings.Contains(w.Body.String(), `"RetCode":3002`) {
		t.Fatalf("stale delete: %d", w.Code)
	}
	form.Set("anchor", restored)
	if w := f.req("POST", base+"/restore", form); w.Code != 303 || !strings.Contains(w.Header().Get("Location"), "result=unchanged") {
		t.Fatal("identical restore not a no-op")
	}
	w = f.libraryAPI("DELETE", apiVersion(), "")
	if w.Code != 204 || w.Body.Len() != 0 || w.Header().Get("Location") != "" {
		t.Fatalf("delete API should succeed without a body or redirect: %d", w.Code)
	}
	trash := f.req("GET", "/web/libraries/"+lib+"/trash", nil)
	if trash.Code != 200 || !strings.Contains(trash.Body.String(), "放回版本列表") || !strings.Contains(trash.Body.String(), first[:12]) || strings.Contains(trash.Body.String(), `name="destination"`) {
		t.Fatal("deleted revision must identify its version and offer timeline restore only")
	}
	if w := f.libraryAPI("DELETE", apiVersion(), ""); w.Code != 404 {
		t.Fatalf("already deleted: %d", w.Code)
	}
	history, err := f.config.Catalog.FileHistory(ctx, user.ID, lib, restored, form.Get("path"), "")
	if err != nil || len(history.Versions) != 2 || history.Versions[0].CommitID != restored || history.Versions[1].CommitID != head {
		t.Fatal("delete removed wrong timeline entry")
	}
	if w := f.req("GET", download(), nil); w.Code != 404 {
		t.Fatalf("deleted download: %d", w.Code)
	}
	if w := f.req("POST", base+"/restore", form); w.Code != 404 {
		t.Fatalf("deleted restore: %d", w.Code)
	}
	form.Set("path", "sibling")
	if w := f.req("GET", download(), nil); w.Code != 200 {
		t.Fatal("delete affected other path in same commit")
	}
	if w := f.req("POST", base+"/delete", form); w.Code != 303 {
		t.Fatalf("standalone form fallback: %d", w.Code)
	}
	item, err = f.db.GetLibrary(ctx, user.ID, lib)
	if err != nil || *item.HeadCommitID != restored || item.HeadVersion != 3 {
		t.Fatal("delete or no-op changed Head")
	}
}
