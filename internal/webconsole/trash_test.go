package webconsole_test

import (
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/mingming-cn/filecloud/internal/auth"
	"github.com/mingming-cn/filecloud/internal/storage"
)

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
