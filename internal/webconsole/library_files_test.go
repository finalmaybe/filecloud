package webconsole_test

import (
	"bytes"
	"fmt"
	"html"
	"net/url"
	"regexp"
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
	created := f.req("POST", "/web/libraries", url.Values{"id": {id}, "name": {"Files"}})
	if created.Code != 303 {
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

	base := created.Header().Get("Location")
	list := f.req("GET", "/web/libraries", nil)
	if list.Code != 200 || !strings.Contains(list.Body.String(), `aria-controls="info-dialog"`) || !strings.Contains(list.Body.String(), `<code id="library-id">`+id+`</code>`) || !strings.Contains(list.Body.String(), `<code id="head-id">`+commit+`</code>`) {
		t.Fatal("library list missing info dialog trigger or published library metadata")
	}
	page := f.req("GET", base, nil)
	if strings.Contains(page.Body.String(), `id="device-guide"`) || strings.Contains(page.Body.String(), `id="library-id"`) {
		t.Fatal("file list still contains library information or binding guide")
	}
	if page.Code != 200 || !strings.Contains(page.Body.String(), "文件列表") || !strings.Contains(page.Body.String(), `href="?commit=`+commit+`&amp;path=docs"`) {
		t.Fatal("detail page did not list the published directory")
	}
	if strings.Contains(page.Body.String(), `class="row-actions"`) {
		t.Fatal("directory-only listing should not offer file actions")
	}
	page = f.req("GET", base+"?commit="+commit+"&path=docs", nil)
	_, actions, _ := strings.Cut(page.Body.String(), `<details class="row-actions"`)
	menu, _, _ := strings.Cut(actions, `</details>`)
	if !strings.Contains(menu, `href="`+base+`/file-history?commit=`+commit) {
		t.Fatal("file actions should link to history at the listed commit")
	}
	historyLinks := regexp.MustCompile(`href="(`+regexp.QuoteMeta(base+"/file-history?")+`[^"]+)"`).FindAllStringSubmatch(page.Body.String(), -1)
	if len(historyLinks) != 2 || historyLinks[0][1] != historyLinks[1][1] {
		t.Fatal("file name and menu should open the same file history")
	}
	page = f.req("GET", html.UnescapeString(historyLinks[0][1]), nil)
	if page.Code != 200 || !strings.Contains(html.UnescapeString(page.Body.String()), "docs/A+B & C.txt") || !strings.Contains(page.Body.String(), file) {
		t.Fatal("expected file details at the selected path and version")
	}

	touched := put("directories", strings.Replace(fmt.Sprintf(dir, file, "A+B & C.txt", "File"), "00:00:00Z", "00:00:01Z", 1))
	root = put("directories", fmt.Sprintf(dir, touched, "docs", "Directory"))
	head := put("commits", fmt.Sprintf(`{"AuthorUserId":%q,"CreatedAt":"2026-08-09T00:00:01Z","DeviceId":%q,"Message":"touch","Parents":[%q],"Root":%q,"Type":"Commit","Version":1}`, user.ID, id, commit, root))
	if _, err := f.db.UpdateLibraryHead(ctx, user.ID, id, &commit, 1, head, nil, f.now); err != nil {
		t.Fatal(err)
	}
	q := url.Values{"path": {"docs/A+B & C.txt"}, "commit": {head}, "panel": {"1"}}
	panel := f.req("GET", base+"/file-history?"+q.Encode(), nil)
	body := panel.Body.String()
	if panel.Code != 200 || strings.Contains(body, "<html") || !strings.Contains(body, "当前版本") || !strings.Contains(body, "修改时间变更") {
		t.Fatal("expected a sidebar fragment for the selected file")
	}
	if !strings.Contains(body, commit) || strings.Contains(body, "设备端恢复预览") || strings.Contains(body, "filecloud library restore") {
		t.Fatal("expected older revision details without a device restore preview")
	}
	for _, legacy := range []string{base + "/history", base + "/snapshot/" + commit} {
		if strings.Contains(body, legacy) || f.req("GET", legacy, nil).Code != 404 {
			t.Fatal("removed history or snapshot view still reachable")
		}
	}
}
