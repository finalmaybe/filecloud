package officepreview

import (
	"archive/zip"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMedia(t *testing.T) {
	for name, want := range map[string]string{
		"共享/报告.DoCX":  Word,
		"预算.XLSX":     Excel,
		"报告.doc":      "",
		"预算.xlsm":     "",
		"预算.xlsx.pdf": "",
		"xlsx":        "",
	} {
		if got := Media(name); got != want {
			t.Errorf("Media(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestReadFallback(t *testing.T) {
	emptyZIP := makeZIP(t, nil)
	for _, tc := range []struct {
		name, media, notice string
		data                []byte
	}{
		{"unsupported format", "application/msword", "暂不支持预览", nil},
		{"empty file", Word, "空文件", nil},
		{"empty archive Word", Word, "暂不支持预览", emptyZIP},
		{"empty archive Excel", Excel, "暂不支持预览", emptyZIP},
		{"not an archive", Word, "暂不支持预览", []byte("not a ZIP")},
		{"encrypted OLE package", Excel, "加密", []byte("\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := readPreview(t, tc.media, tc.data)
			if !strings.Contains(got.Notice, tc.notice) || got.Text != "" || len(got.Sheets) != 0 {
				t.Fatalf("fallback = %+v, want %q and no content", got, tc.notice)
			}
		})
	}
}

func TestReadSourceError(t *testing.T) {
	want := errors.New("source read failed")
	r := io.MultiReader(strings.NewReader("part of a file"), errorReader{want})
	got, err := Read(r, Word, -1)
	if !errors.Is(err, want) || got.Text != "" || got.Notice != "" || len(got.Sheets) != 0 {
		t.Fatalf("Read = (%+v, %v), want only the source error", got, err)
	}
}

func TestReadInputLimit(t *testing.T) {
	for _, tc := range []struct {
		name  string
		size  int64
		reads int
	}{
		{"known oversized", inputLimit + 1, 0},
		{"unknown size", -1, inputLimit + 1},
		{"understated size", 1, inputLimit + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := &countingZeros{}
			got, err := Read(r, Excel, tc.size)
			if err != nil || !strings.Contains(got.Notice, "12 MiB") || got.Text != "" || len(got.Sheets) != 0 || r.read != tc.reads {
				t.Fatalf("Read = (%+v, %v), consumed %d bytes, want limit notice and %d bytes", got, err, r.read, tc.reads)
			}
		})
	}
}

func TestReadUnzipLimit(t *testing.T) {
	for _, tc := range []struct {
		media, part string
		limit       int
	}{
		{Word, wordPart, wordXMLLimit},
		{Excel, "xl/media/large.bin", excelUnzipLimit / 2},
	} {
		t.Run(tc.part, func(t *testing.T) {
			parts := map[string]string{tc.part: strings.Repeat("a", tc.limit+1)}
			if tc.media == Excel {
				parts["xl/media/also-large.bin"] = strings.Repeat("b", tc.limit)
			}
			data := makeZIP(t, parts)
			if len(data) >= inputLimit {
				t.Fatal("fixture exceeded the compressed input limit")
			}
			got := readPreview(t, tc.media, data)
			if !strings.Contains(got.Notice, "解包后") || got.Text != "" || len(got.Sheets) != 0 {
				t.Fatalf("preview = %+v, want unpacking limit notice with no content", got)
			}
		})
	}
}

func TestReadWordBody(t *testing.T) {
	body := `<w:p><w:r><w:t xml:space="preserve"> 报告 &amp; 明细 </w:t><w:tab/><w:t>甲</w:t><w:br/><w:t>乙</w:t><w:cr/><w:instrText>忽略域代码</w:instrText><w:delText>忽略删除内容</w:delText></w:r><w:hyperlink><w:r><w:t>链接标签</w:t></w:r></w:hyperlink></w:p>` +
		`<w:p/><w:tbl><w:tr><w:tc><w:p><w:r><w:t>表格</w:t></w:r></w:p></w:tc></w:tr></w:tbl>`
	for _, ns := range []string{wordNamespace, strictWordNamespace} {
		got := readPreview(t, Word, makeZIP(t, map[string]string{
			wordPart:                       wordXML(ns, body),
			"word/header1.xml":             "not even XML (not read)",
			"word/_rels/document.xml.rels": `<Relationships><Relationship TargetMode="External" Target="https://example.invalid/"/></Relationships>`,
		}))
		if got.Text != " 报告 & 明细 \t甲\n乙\n链接标签\n\n表格" || got.Notice != "" || len(got.Sheets) != 0 {
			t.Fatalf("Word preview = %+v", got)
		}
	}
}

func TestReadWordEmptyAndBadBody(t *testing.T) {
	for _, tc := range []struct{ name, xml, notice string }{
		{"empty body", wordXML(wordNamespace, `<w:p/>`), "没有可预览的正文"},
		{"wrong root", `<unrelated/>`, "暂不支持预览"},
		{"wrong namespace", wordXML("urn:other", ""), "暂不支持预览"},
		{"missing body", `<w:document xmlns:w="` + wordNamespace + `"/>`, "暂不支持预览"},
		{"bad XML after content", wordXML(wordNamespace, `<w:p><w:r><w:t>合法前缀</w:t></w:r></w:p>`) + `<`, "暂不支持预览"},
		{"deep XML", wordXML(wordNamespace, strings.Repeat(`<a>`, 129)+strings.Repeat(`</a>`, 129)), "暂不支持预览"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := readPreview(t, Word, makeZIP(t, map[string]string{wordPart: tc.xml}))
			if !strings.Contains(got.Notice, tc.notice) || got.Text != "" || len(got.Sheets) != 0 {
				t.Fatalf("fallback = %+v, want %q and no content", got, tc.notice)
			}
		})
	}
}

func TestReadWordTextLimit(t *testing.T) {
	prefix := strings.Repeat("a", textLimit-1)
	data := makeZIP(t, map[string]string{wordPart: wordXML(wordNamespace, `<w:p><w:r><w:t>`+prefix+`界b</w:t></w:r></w:p>`)})
	got := readPreview(t, Word, data)
	if got.Text != prefix || !utf8.ValidString(got.Text) || !strings.Contains(got.Notice, "截断") {
		t.Fatalf("text bytes = %d, valid UTF-8 = %v, notice = %q", len(got.Text), utf8.ValidString(got.Text), got.Notice)
	}
}

func readPreview(t *testing.T, media string, data []byte) Document {
	t.Helper()
	got, err := Read(bytes.NewReader(data), media, int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func makeZIP(t *testing.T, parts map[string]string) []byte {
	t.Helper()
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for name, data := range parts {
		writeZIPPart(t, w, name, data)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func writeZIPPart(t *testing.T, w *zip.Writer, name, data string) {
	t.Helper()
	p, err := w.Create(name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(p, data); err != nil {
		t.Fatal(err)
	}
}

func wordXML(namespace, body string) string {
	return `<w:document xmlns:w="` + namespace + `"><w:body>` + body + `</w:body></w:document>`
}

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

type countingZeros struct{ read int }

func (r *countingZeros) Read(p []byte) (int, error) {
	clear(p)
	r.read += len(p)
	return len(p), nil
}
