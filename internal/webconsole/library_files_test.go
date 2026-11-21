package webconsole_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"strings"
	"testing"

	"github.com/mingming-cn/filecloud/internal/auth"
	"github.com/mingming-cn/filecloud/internal/object"
)

func TestFileListOpensFileScopedVersions(t *testing.T) {
	f := newFixture(t)
	f.login()
	ctx := t.Context()
	user, err := f.db.FindUserByUsername(ctx, "observer")
	if err != nil {
		t.Fatal(err)
	}
	id, _ := auth.NewUserID(nil)
	created := f.libraryAPI("PUT", "/"+id, `{"Name":"Files"}`)
	if created.Code != 201 {
		t.Fatalf("create status %d", created.Code)
	}
	put := func(kind, data string) string {
		t.Helper()
		canonical, hash, err := object.Canonicalize(kind, []byte(data))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.PutObject(ctx, user.ID, id, kind, hash, bytes.NewReader(canonical)); err != nil {
			t.Fatal(err)
		}
		return hash
	}
	file := put("files", `{"Blocks":[],"Size":"0","Type":"File","Version":1}`)
	const dir = `{"Entries":[{"Id":%q,"ModifiedAt":"2026-08-09T00:00:00Z","Name":%q,"Type":%q}],"Type":"Directory","Version":1}`
	sub := put("directories", fmt.Sprintf(dir, file, "A+B & C.txt", "File"))
	root := put("directories", fmt.Sprintf(dir, sub, "docs", "Directory"))
	commit := put("commits", fmt.Sprintf(`{"AuthorUserId":%q,"CreatedAt":"2026-08-09T00:00:00Z","DeviceId":%q,"Message":"first version","Parents":[],"Root":%q,"Type":"Commit","Version":1}`, user.ID, id, root))
	if _, err := f.db.UpdateLibraryHead(ctx, user.ID, id, nil, 0, commit, nil, f.now); err != nil {
		t.Fatal(err)
	}

	base := "/web/libraries/" + id
	list := f.libraryAPI("GET", "", "")
	if list.Code != 200 || !strings.Contains(list.Body.String(), `"LibraryId":"`+id+`"`) || !strings.Contains(list.Body.String(), `"HeadCommitId":"`+commit+`"`) {
		t.Fatal("library list missing published library metadata")
	}
	page := f.req("GET", base, nil)
	if strings.Contains(page.Body.String(), `id="device-guide"`) || strings.Contains(page.Body.String(), `id="library-id"`) {
		t.Fatal("file list still contains library information or binding guide")
	}
	if page.Code != 200 || !strings.Contains(page.Body.String(), `data-api="/v1/libraries/`+id+`/files"`) || !strings.Contains(page.Body.String(), `/web/assets/files.js`) {
		t.Fatal("file list page did not connect to the directory API")
	}
	files := f.libraryAPI("GET", "/"+id+"/files?Path=docs", "")
	var listing struct {
		Directory struct {
			CommitID string `json:"CommitId"`
			Path     string
			Entries  []struct{ Name, Type string }
		}
	}
	if files.Code != 200 || json.Unmarshal(files.Body.Bytes(), &listing) != nil {
		t.Fatal("expected directory JSON with a browser session")
	}
	d := listing.Directory
	if d.CommitID != commit || d.Path != "docs" || len(d.Entries) != 1 || d.Entries[0].Type != "File" || d.Entries[0].Name != "A+B & C.txt" {
		t.Fatal("wrong published file listing")
	}
	link := url.Values{"commit": {d.CommitID}, "path": {d.Path + "/" + d.Entries[0].Name}}
	page = f.req("GET", base+"/file-history?"+link.Encode(), nil)
	if page.Code != 200 || !strings.Contains(html.UnescapeString(page.Body.String()), "docs/A+B & C.txt") || !strings.Contains(page.Body.String(), file) {
		t.Fatal("expected file details at the selected path and version")
	}

	touched := put("directories", strings.Replace(fmt.Sprintf(dir, file, "A+B & C.txt", "File"), "00:00:00Z", "00:00:01Z", 1))
	root = put("directories", fmt.Sprintf(dir, touched, "docs", "Directory"))
	head := put("commits", fmt.Sprintf(`{"AuthorUserId":%q,"CreatedAt":"2026-08-09T00:00:01Z","DeviceId":%q,"Message":"touch","Parents":[%q],"Root":%q,"Type":"Commit","Version":1}`, user.ID, id, commit, root))
	if _, err := f.db.UpdateLibraryHead(ctx, user.ID, id, &commit, 1, head, nil, f.now); err != nil {
		t.Fatal(err)
	}
	// This publication changes the directory's timestamp, not the selected file.
	root = put("directories", strings.Replace(fmt.Sprintf(dir, touched, "docs", "Directory"), "00:00:00Z", "00:00:02Z", 1))
	anchor := put("commits", fmt.Sprintf(`{"AuthorUserId":%q,"CreatedAt":"2026-08-09T00:00:02Z","DeviceId":%q,"Message":"directory only","Parents":[%q],"Root":%q,"Type":"Commit","Version":1}`, user.ID, id, head, root))
	if _, err := f.db.UpdateLibraryHead(ctx, user.ID, id, &head, 2, anchor, nil, f.now); err != nil {
		t.Fatal(err)
	}
	apiQuery := url.Values{"Path": {"docs/A+B & C.txt"}, "CommitId": {anchor}}
	api := f.libraryAPI("GET", "/"+id+"/file-history?"+apiQuery.Encode(), "")
	var result struct {
		FileHistory struct {
			AnchorCommitId, Path string
			Current              struct{ CommitId, FileId string }
			Versions             []struct {
				CommitId, Change string
				IsCurrent        bool
			}
		}
	}
	if api.Code != 200 || !strings.HasPrefix(api.Header().Get("Content-Type"), "application/json") || json.Unmarshal(api.Body.Bytes(), &result) != nil {
		t.Fatal("expected file history JSON using a Web session")
	}
	h := result.FileHistory
	if h.AnchorCommitId != anchor || h.Current.CommitId != anchor || h.Current.FileId != file || h.Path != "docs/A+B & C.txt" || len(h.Versions) != 2 || h.Versions[0].Change != "modified-time" || h.Versions[1].CommitId != commit || !h.Versions[0].IsCurrent || h.Versions[1].IsCurrent {
		t.Fatal("incorrect sidebar file history")
	}
	q := url.Values{"path": {h.Path}, "commit": {h.AnchorCommitId}, "panel": {"1"}}
	standalone := f.req("GET", base+"/file-history?"+q.Encode(), nil)
	body := standalone.Body.String()
	if standalone.Code != 200 || !strings.Contains(body, "<!doctype html>") || !strings.Contains(body, h.Current.FileId) || !strings.Contains(body, "修改时间变更") {
		t.Fatal("even a legacy panel link should open the full page at the same version")
	}
	if !strings.Contains(body, commit) || strings.Contains(body, "设备端恢复预览") || strings.Contains(body, "filecloud library restore") {
		t.Fatal("expected older revision details without a device restore preview")
	}
	for _, version := range []string{head, commit} {
		menu := "version-menu-" + version
		_, after, found := strings.Cut(body, `id="`+menu+`"`)
		form, _, _ := strings.Cut(after, "</form>")
		if !found || !strings.Contains(body, `popovertarget="`+menu+`"`) || !strings.Contains(form, `name="commit" value="`+version+`"`) || !strings.Contains(form, "下载版本") {
			t.Fatal("expected a version menu targeting its own revision")
		}
		for _, action := range []string{"恢复版本", "删除版本"} {
			if strings.Contains(form, action) != (version == commit) {
				t.Fatalf("only the older file version should offer %s", action)
			}
		}
	}
	for _, legacy := range []string{base + "/history", base + "/snapshot/" + commit} {
		if strings.Contains(body, legacy) || f.req("GET", legacy, nil).Code != 404 {
			t.Fatal("removed history or snapshot view still reachable")
		}
	}
}
