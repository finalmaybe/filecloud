package webconsole_test

import (
	"net/http/httptest"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/mingming-cn/filecloud/internal/auth"
	"github.com/mingming-cn/filecloud/internal/storage"
)

func TestDeleteTrashFormRequiresBrowserSessionAndOrigin(t *testing.T) {
	d := newDrive(t)
	d.call(201, "PUT", "selected.txt", "data")
	d.call(201, "MKCOL", "folder/", "")
	d.call(201, "PUT", "keep.txt", "data")
	base := "/libraries/" + d.lib + "/trash"
	for _, name := range []string{"selected.txt", "folder", "keep.txt"} {
		lib, err := d.db.GetLibrary(t.Context(), d.owner, d.lib)
		if err != nil || lib.HeadCommitID == nil {
			t.Fatal("missing fixture head", err)
		}
		if w := d.req("POST", base+"/move", url.Values{"path": {name}, "commit": {*lib.HeadCommitID}}); w.Code != 303 {
			t.Fatalf("move %s to trash: %d", name, w.Code)
		}
	}
	list := func() []storage.TrashEntry {
		t.Helper()
		items, err := d.config.Catalog.ListTrash(t.Context(), d.owner, d.lib, 0)
		if err != nil {
			t.Fatal(err)
		}
		return items
	}
	items := list()
	if len(items) != 3 {
		t.Fatalf("expected bin records for the three removed paths: %+v", items)
	}
	page := d.req("GET", base, nil)
	if page.Code != 200 || strings.Count(page.Body.String(), `class="trash-select"`) != 3 ||
		!strings.Contains(page.Body.String(), `id="trash-delete-dialog"`) || !strings.Contains(page.Body.String(), base+`/restore`) {
		t.Fatal("trash page missing selection, deletion confirmation, or restore", page.Code)
	}
	selected := []string{strconv.FormatInt(items[1].ID, 10), strconv.FormatInt(items[2].ID, 10)}
	form := url.Values{"id": selected, "before": {strconv.FormatInt(items[0].ID+1, 10)}, "filter": {"s&x"}}
	post := func(source string, withCookie bool) int {
		t.Helper()
		r := httptest.NewRequest("POST", origin+base+"/delete", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if source != "" {
			r.Header.Set("Origin", source)
		}
		if withCookie {
			r.AddCookie(d.cookie)
		}
		w := httptest.NewRecorder()
		d.web.ServeHTTP(w, r)
		return w.Code
	}
	if post(origin, false) != 401 || post("", true) != 403 || post("https://other.example", true) != 403 {
		t.Fatal("missing Web session or same-origin protection")
	}
	if w := d.libraryAPI("POST", "/"+d.lib+"/trash/delete", `{"IDs":[`+strings.Join(selected, ",")+`]}`); w.Code != 401 {
		t.Fatal("Bearer endpoint accepted the Web session", w.Code)
	}
	if w := d.req("POST", base+"/delete", url.Values{"id": {selected[0], selected[0]}}); w.Code != 400 {
		t.Fatal("duplicate IDs accepted", w.Code)
	}
	if w := d.req("POST", base+"/delete", url.Values{"id": {selected[0], "999999"}}); w.Code != 404 || !strings.Contains(w.Body.String(), "未删除任何条目") {
		t.Fatal("missing stale-batch feedback", w.Code)
	}
	if items := list(); len(items) != 3 {
		t.Fatal("rejected requests changed trash", items)
	}
	w := d.req("POST", base+"/delete", form)
	location := base + "?" + url.Values{"before": form["before"], "deleted": {"1"}, "filter": form["filter"]}.Encode()
	if w.Code != 303 || w.Header().Get("Location") != location {
		t.Fatal("expected redirect retaining page and filter", w.Code, w.Header().Get("Location"))
	}
	page = d.req("GET", location, nil)
	if page.Code != 200 || !strings.Contains(page.Body.String(), "已永久删除") || !strings.Contains(page.Body.String(), `value="s&amp;x"`) {
		t.Fatal("missing success feedback or retained filter", page.Code)
	}
	if left := list(); len(left) != 1 || left[0].ID != items[0].ID || left[0].Path != "keep.txt" {
		t.Fatal("deleted an unselected record", left)
	}
}

func TestLibraryRetentionCreationPreviewAndDefaults(t *testing.T) {
	f := newFixture(t)
	f.login()
	ctx := t.Context()
	if err := f.db.SetDefaults(ctx, storage.Retention{TrashDays: 45, HistoryDays: 90}, f.now); err != nil {
		t.Fatal(err)
	}
	page := f.req("GET", "/libraries", nil)
	for _, s := range []string{"跟随全局默认（45 天）", "跟随全局默认（90 天）"} {
		if !strings.Contains(page.Body.String(), s) {
			t.Fatal("creation form missing", s)
		}
	}
	lib, _ := auth.NewUserID(nil)
	for _, bad := range []string{`{"Name":"x","TrashDays":0}`, `{"Name":"x","HistoryDays":null}`, `{"Name":"x","HistoryDays":1,"HistoryDays":2}`, `{"Name":"x","TrashDays":36501}`} {
		if w := f.libraryAPI("PUT", "/"+lib, bad); w.Code != 400 {
			t.Fatal(bad, w.Code)
		}
	}
	if w := f.libraryAPI("PUT", "/"+lib, `{"Name":"test","HistoryDays":10}`); w.Code != 201 {
		t.Fatal(w.Code)
	}
	user, err := f.db.FindUserByUsername(ctx, "observer")
	if err != nil {
		t.Fatal(err)
	}
	get := func() storage.Library {
		l, err := f.db.GetLibrary(ctx, user.ID, lib)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	if got := get().Retention; got != (storage.Retention{HistoryDays: 10}) {
		t.Fatal(got)
	}
	base := "/libraries/" + lib
	if w := f.req("GET", base+"/trash", nil); w.Code != 200 || !strings.Contains(w.Body.String(), "回收站为空") {
		t.Fatal("empty trash", w.Code)
	}
	form := url.Values{"history": {"5"}, "trash": {"12"}}
	w := f.req("POST", base+"/retention", form)
	m := regexp.MustCompile(`name="confirm" value="([a-f0-9]{64})"`).FindStringSubmatch(w.Body.String())
	if w.Code != 200 || len(m) != 2 || get().HistoryDays != 10 {
		t.Fatal("preview did not require confirmation", w.Code)
	}
	form.Set("confirm", m[1])
	if w := f.req("POST", base+"/retention", form); w.Code != 303 || get().HistoryDays != 5 || get().TrashDays != 12 {
		t.Fatal("confirm", w.Code)
	}
	if w := f.req("POST", base+"/retention", url.Values{"trash": {"1"}}); w.Code != 303 || get().HistoryDays != 0 {
		t.Fatal("return to global default", w.Code)
	}
}
