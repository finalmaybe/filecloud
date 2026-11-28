package webconsole_test

import (
	"bytes"
	"fmt"
	"html"
	"mime"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/mingming-cn/filecloud/internal/auth"
	"github.com/mingming-cn/filecloud/internal/object"
)

func TestPublishedFilePreviews(t *testing.T) {
	f := newFixture(t)
	f.login()
	ctx := t.Context()
	owner, err := f.db.FindUserByUsername(ctx, "observer")
	if err != nil {
		t.Fatal(err)
	}
	lib, _ := auth.NewUserID(nil)
	if w := f.libraryAPI("PUT", "/"+lib, `{"Name":"Previews"}`); w.Code != 201 {
		t.Fatal(w.Code)
	}
	put := func(kind string, data []byte) string {
		t.Helper()
		id := object.ID(data)
		if _, err := f.db.PutObject(ctx, owner.ID, lib, kind, id, bytes.NewReader(data)); err != nil {
			t.Fatal(err)
		}
		return id
	}
	const source = "\n  <em>中文 & text</em>\n"
	const stamp = "2026-08-09T00:00:00Z"
	pdf := append([]byte("%PDF-1.4\n"), bytes.Repeat([]byte{' '}, object.MaxBlockSize)...)
	pdf = append(pdf, "%%EOF\n"...)
	files := map[string][]byte{
		"A+B & C.HTML": []byte(source), "sample.svg": []byte(source), "notes.md": []byte(source),
		"image.JPEG": {0xff, 0xd8, 0xff, 0xdb}, "image.png": []byte("\x89PNG\r\n\x1a\n"),
		"image.webp": []byte("RIFF\x08\x00\x00\x00WEBPVP8 "), "image.gif": []byte("GIF89a"),
		"doc.pdf": pdf, "wrong.pdf": []byte(source), "bad.png": []byte(source), "notes.docx": []byte(source),
		"empty.txt": {}, "binary.txt": {0x00, 0x01}, "invalid.txt": {0xef},
		"large.log": []byte(strings.Repeat("a", (256<<10)-1) + "界unshown"),
	}
	for _, name := range []string{"sample.docx", "sample.xlsx"} {
		files[name], err = os.ReadFile("../officepreview/testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
	}
	var entries []object.DirectoryEntry
	for name, data := range files {
		blocks := []string{}
		for p := range slices.Chunk(data, object.MaxBlockSize) {
			blocks = append(blocks, fmt.Sprintf("%q", put("blocks", p)))
		}
		file := put("files", fmt.Appendf(nil, `{"Blocks":[%s],"Size":"%d","Type":"File","Version":1}`, strings.Join(blocks, ","), len(data)))
		entries = append(entries, object.DirectoryEntry{Name: name, Type: "File", ID: file, ModifiedAt: stamp})
	}
	slices.SortFunc(entries, func(a, b object.DirectoryEntry) int { return strings.Compare(a.Name, b.Name) })
	dir := put("directories", object.Directory{Entries: entries}.Bytes())
	root := put("directories", object.Directory{Entries: []object.DirectoryEntry{{Name: "docs", Type: "Directory", ID: dir, ModifiedAt: stamp}}}.Bytes())
	commit := object.Commit{Root: root, AuthorUserID: owner.ID, DeviceID: lib, CreatedAt: stamp}
	first := put("commits", commit.Bytes())
	if _, err := f.db.UpdateLibraryHead(ctx, owner.ID, lib, nil, 0, first, nil, f.now); err != nil {
		t.Fatal(err)
	}
	commit.Parents = []string{first}
	head := put("commits", commit.Bytes()) // The listed commit did not change any of these files.
	if _, err := f.db.UpdateLibraryHead(ctx, owner.ID, lib, &first, 1, head, nil, f.now); err != nil {
		t.Fatal(err)
	}
	link := func(commit, path, mode string) string {
		return "/libraries/" + lib + "/file-preview?" + url.Values{"commit": {commit}, "path": {path}, "mode": {mode}}.Encode()
	}
	get := func(name, mode string) *httptest.ResponseRecorder {
		return f.req("GET", link(head, "docs/"+name, mode), nil)
	}
	for _, name := range []string{"A+B & C.HTML", "sample.svg", "notes.md"} {
		w := get(name, "")
		body := w.Body.String()
		if w.Code != 200 || !strings.Contains(html.UnescapeString(body), "<code>"+source+"</code>") || strings.Contains(body, "<em>") || !strings.Contains(body, "?commit="+head+"&amp;path=docs\"") {
			t.Errorf("%s: expected escaped source and a pinned link back to the directory", name)
		}
		if raw := get(name, "inline"); raw.Code != 415 || strings.Contains(raw.Body.String(), source) {
			t.Errorf("%s: allowed inline source", name)
		}
	}
	for _, name := range []string{"A+B & C.HTML", "image.png", "doc.pdf", "notes.docx", "sample.docx", "sample.xlsx"} {
		stage := get(name, "stage")
		markup := strings.TrimSpace(stage.Body.String())
		page := get(name, "")
		if stage.Code != 200 || markup == "" || !strings.Contains(page.Body.String(), markup) || strings.Contains(markup, "<!doctype") || strings.Contains(markup, "<em>") {
			t.Errorf("%s: expected the same escaped stage in the dialog and standalone page", name)
		}
		if (name == "image.png" || name == "doc.pdf") && !strings.Contains(markup, `/libraries/`+lib+`/file-preview?commit=`+head) {
			t.Errorf("%s: fragment media link must resolve at the pinned preview, not at the listing URL", name)
		}
		for _, label := range []string{`aria-label="缩小"`, `aria-label="放大"`, `>原始尺寸</button>`, `>适应窗口</button>`} {
			if strings.Contains(markup, label) != (name == "image.png") {
				t.Errorf("%s: unexpected presence or absence of image zoom control %s", name, label)
			}
		}
	}
	for name, markers := range map[string][]string{
		"sample.docx": {`class="office-word"`, "季度摘要", "&lt;b&gt;原文&lt;/b&gt;"},
		"sample.xlsx": {"<summary>预算</summary>", "<summary>汇总</summary>", "<td>&lt;b&gt;原文&lt;/b&gt;</td><td></td><td>42</td>"},
	} {
		stage := get(name, "stage").Body.String()
		for _, marker := range markers {
			if !strings.Contains(stage, marker) {
				t.Errorf("%s: missing %q in preview", name, marker)
			}
		}
		if w := get(name, "inline"); w.Code != 415 {
			t.Errorf("%s: allowed raw inline Office content", name)
		}
		if w := get(name, "download"); w.Code != 200 || !bytes.Equal(w.Body.Bytes(), files[name]) {
			t.Errorf("%s: download changed the original file", name)
		}
	}
	listing := f.req("GET", "/libraries/"+lib, nil)
	if !strings.Contains(listing.Body.String(), `<dialog class="viewer"`) || !strings.Contains(listing.Header().Get("Content-Security-Policy"), "frame-src 'self'") || listing.Header().Get("X-Frame-Options") != "DENY" {
		t.Error("listing must host the modal and allow embedding a same-origin PDF without becoming frameable")
	}
	for name, media := range map[string]string{"image.JPEG": "image/jpeg", "image.webp": "image/webp", "image.gif": "image/gif", "image.png": "image/png", "doc.pdf": "application/pdf"} {
		w := get(name, "")
		marker := `aria-label="图片缩放"`
		if media == "application/pdf" {
			marker = "<iframe"
		}
		if w.Code != 200 || !strings.Contains(w.Body.String(), marker) || !strings.Contains(w.Body.String(), "下载原文件") {
			t.Errorf("missing %s viewer or download", media)
		}
		w = get(name, "inline")
		if w.Code != 200 || w.Header().Get("Content-Type") != media || !bytes.Equal(w.Body.Bytes(), files[name]) || w.Header().Get("X-Content-Type-Options") != "nosniff" || w.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("wrong %s response", media)
		}
	}
	for _, name := range []string{"bad.png", "wrong.pdf", "notes.docx"} {
		if w := get(name, ""); w.Code != 200 || !strings.Contains(w.Body.String(), "暂不支持预览") {
			t.Errorf("%s: missing fallback", name)
		}
		if w := get(name, "inline"); w.Code != 415 {
			t.Errorf("%s: inline status %d", name, w.Code)
		}
	}
	for _, name := range []string{"invalid.txt", "binary.txt"} {
		if w := get(name, ""); !strings.Contains(w.Body.String(), "不是可预览的 UTF-8 文本") {
			t.Errorf("%s: expected encoding message", name)
		}
	}
	if w := get("empty.txt", ""); !strings.Contains(w.Body.String(), "空文件") {
		t.Error("empty file missing message")
	}
	if w := get("large.log", ""); !strings.Contains(w.Body.String(), "仅显示前 256 KiB") || !strings.Contains(w.Body.String(), "aaaa</code>") || strings.Contains(w.Body.String(), "unshown") {
		t.Error("text prefix did not end at a character boundary")
	}
	w := get("A+B & C.HTML", "download")
	disposition, params, err := mime.ParseMediaType(w.Header().Get("Content-Disposition"))
	if err != nil || disposition != "attachment" || params["filename"] != "A+B & C.HTML" || w.Header().Get("Content-Type") != "application/octet-stream" || w.Body.String() != source {
		t.Error("incorrect original download")
	}
	r := httptest.NewRequest("GET", origin+link(head, "docs/doc.pdf", "inline"), nil)
	r.AddCookie(f.cookie)
	r.Header.Set("Range", fmt.Sprintf("bytes=%d-", object.MaxBlockSize-1))
	w = httptest.NewRecorder()
	f.web.ServeHTTP(w, r)
	if w.Code != 206 || !bytes.Equal(w.Body.Bytes(), pdf[object.MaxBlockSize-1:]) {
		t.Error("incorrect cross-block PDF range")
	}
	for _, tc := range []struct {
		commit, path string
		status       int
	}{
		{"", "docs/image.png", 400}, {head, "../image.png", 400}, {head, "docs", 404}, {head, "missing", 404}, {object.ID([]byte("unpublished")), "docs/image.png", 404},
	} {
		if w := f.req("GET", link(tc.commit, tc.path, "download"), nil); w.Code != tc.status {
			t.Errorf("invalid target: status %d, want %d", w.Code, tc.status)
		}
	}
	block, _, err := f.db.GetObject(ctx, owner.ID, lib, "blocks", object.ID(pdf[:object.MaxBlockSize]))
	if err != nil {
		t.Fatal(err)
	}
	blockPath := block.Name()
	block.Close()
	if err := os.Chmod(blockPath, 0600); err != nil {
		t.Fatal(err)
	}
	corrupt := bytes.Clone(pdf[:object.MaxBlockSize])
	corrupt[len(corrupt)-1] ^= 1 // The PDF signature still matches.
	if err := os.WriteFile(blockPath, corrupt, 0600); err != nil {
		t.Fatal(err)
	}
	if w := get("doc.pdf", "inline"); w.Code != 500 || w.Header().Get("Content-Type") == "application/pdf" {
		t.Error("released corrupt PDF content")
	}
	f.cookie = nil
	if w := get("doc.pdf", "inline"); w.Code != 303 {
		t.Error("anonymous raw content")
	}
	if err := f.sessions.SetAccountPassword(ctx, "second", f.pass, "test", true); err != nil {
		t.Fatal(err)
	}
	f.cookie = f.req("POST", "/login", url.Values{"username": {"second"}, "password": {f.pass}}).Result().Cookies()[0]
	if w := f.libraryAPI("PUT", "/"+lib, `{"Name":"Other"}`); w.Code != 201 {
		t.Fatal(w.Code)
	}
	for _, mode := range []string{"", "stage", "inline", "download"} {
		if w := get("doc.pdf", mode); w.Code != 404 {
			t.Errorf("foreign snapshot: status %d", w.Code)
		}
	}
}
