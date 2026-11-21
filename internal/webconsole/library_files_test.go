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

func TestFileListLinksToHistoryAndPinnedFile(t *testing.T) {
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
	for _, target := range []string{"file-history", "snapshot/" + commit} {
		pattern := `href="(` + regexp.QuoteMeta(base+"/"+target+"?") + `[^"]+)"`
		match := regexp.MustCompile(pattern).FindStringSubmatch(page.Body.String())
		if len(match) == 0 {
			t.Fatalf("missing %s link", target)
		}
		page = f.req("GET", html.UnescapeString(match[1]), nil)
		if page.Code != 200 || !strings.Contains(html.UnescapeString(page.Body.String()), "docs/A+B & C.txt") {
			t.Fatalf("%s did not open the selected path (status %d)", target, page.Code)
		}
	}
	if !strings.Contains(page.Body.String(), file) {
		t.Fatal("version did not show the file in the selected snapshot")
	}
}
