package officepreview

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"
)

func TestReadExcelFormattedSparseAndCachedValues(t *testing.T) {
	book := newTestWorkbook(t)
	mustWorkbook(t, book.SetSheetName("Sheet1", "账本"))
	mustWorkbook(t, book.SetCellValue("账本", "C2", "条目"))
	mustWorkbook(t, book.SetCellValue("账本", "A4", 0.125))
	style, err := book.NewStyle(&excelize.Style{NumFmt: 10})
	mustWorkbook(t, err)
	mustWorkbook(t, book.SetCellStyle("账本", "A4", "A4", style))
	mustWorkbook(t, book.SetCellFormula("账本", "B4", "1+1"))
	mustWorkbook(t, book.SetCellFormula("账本", "C4", "3+4"))
	addSheet(t, book, "隐藏")
	mustWorkbook(t, book.SetCellValue("隐藏", "A1", "不显示"))
	mustWorkbook(t, book.SetSheetVisible("隐藏", false, true))
	addSheet(t, book, "空表")
	addSheet(t, book, "明细")
	mustWorkbook(t, book.SetCellValue("明细", "B1", "第二表"))
	data := replaceWorksheet(t, workbookBytes(t, book), func(sheetXML string) string {
		const formula = `<f>1+1</f>`
		if !strings.Contains(sheetXML, formula) {
			t.Fatalf("fixture formula absent in worksheet: %s", sheetXML)
		}
		// The cached result is intentionally not 2, so real calculation would fail the test.
		return strings.Replace(sheetXML, formula, formula+`<v>9</v>`, 1)
	})
	got := readPreview(t, Excel, data)
	if got.Text != "" || got.Notice != "" || len(got.Sheets) != 3 {
		t.Fatalf("preview = %+v", got)
	}
	checkSheet(t, got.Sheets[0], "账本", []string{"A", "B", "C"}, [][]string{
		{"", "", ""}, {"", "", "条目"}, {"", "", ""}, {"12.50%", "9", ""},
	})
	checkSheet(t, got.Sheets[1], "空表", nil, nil)
	checkSheet(t, got.Sheets[2], "明细", []string{"A", "B"}, [][]string{{"", "第二表"}})
}

func TestReadExcelEmptyWorkbook(t *testing.T) {
	book := newTestWorkbook(t)
	got := readPreview(t, Excel, workbookBytes(t, book))
	if got.Text != "" || !strings.Contains(got.Notice, "没有可预览的单元格") || len(got.Sheets) != 1 {
		t.Fatalf("preview = %+v", got)
	}
	checkSheet(t, got.Sheets[0], "Sheet1", nil, nil)
}

func TestReadExcelBadWorksheet(t *testing.T) {
	book := newTestWorkbook(t)
	mustWorkbook(t, book.SetCellValue("Sheet1", "A1", "合法前缀"))
	data := workbookBytes(t, book)
	for _, tc := range []struct {
		name   string
		change func(string) string
	}{
		{"incomplete XML after content", func(s string) string { return s + `<` }},
		{"empty XML", func(string) string { return "" }},
		{"wrong root", func(string) string { return `<notworksheet/>` }},
		{"missing sheet data", func(string) string { return `<worksheet/>` }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := readPreview(t, Excel, replaceWorksheet(t, data, tc.change))
			if !strings.Contains(got.Notice, "暂不支持预览") || got.Text != "" || len(got.Sheets) != 0 {
				t.Fatalf("preview = %+v, want unsupported notice with no content", got)
			}
		})
	}
}

func TestReadExcelBadWorksheetChecksum(t *testing.T) {
	book := newTestWorkbook(t)
	data := replaceWorksheet(t, workbookBytes(t, book), func(sheetXML string) string {
		return strings.Replace(sheetXML, `<sheetData></sheetData>`, `<sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>checksum-sentinel</t></is></c></row></sheetData>`, 1)
	})
	start := bytes.Index(data, []byte("checksum-sentinel"))
	if start < 0 {
		t.Fatal("fixture worksheet must contain the uncompressed sentinel")
	}
	valid := readPreview(t, Excel, data)
	if valid.Notice != "" || len(valid.Sheets) != 1 {
		t.Fatalf("valid fixture preview = %+v", valid)
	}
	checkSheet(t, valid.Sheets[0], "Sheet1", []string{"A"}, [][]string{{"checksum-sentinel"}})
	data[start] = 'C' // Leave well-formed XML, but invalidate the ZIP checksum.
	got := readPreview(t, Excel, data)
	if !strings.Contains(got.Notice, "暂不支持预览") || got.Text != "" || len(got.Sheets) != 0 {
		t.Fatalf("preview = %+v, want unsupported notice with no content", got)
	}
}

func TestReadExcelRange(t *testing.T) {
	book := newTestWorkbook(t)
	mustWorkbook(t, book.SetCellValue("Sheet1", "A1", "start"))
	mustWorkbook(t, book.SetCellValue("Sheet1", "Z100", "last"))
	data := workbookBytes(t, book)
	got := readPreview(t, Excel, data)
	if got.Notice != "" || len(got.Sheets) != 1 || len(got.Sheets[0].Rows) != 100 || len(got.Sheets[0].Columns) != 26 || got.Sheets[0].Rows[99][25] != "last" {
		t.Fatalf("exact preview range not retained: notice = %q, sheets = %d", got.Notice, len(got.Sheets))
	}

	for _, tc := range []struct{ name, marker, insertion string }{
		{"extra rows", `</sheetData>`, `<row r="1048576"><c r="B1048576" t="inlineStr"><is><t>clipped</t></is></c></row>`},
		{"extra columns", `</row></sheetData>`, `<c r="XFD100" t="inlineStr"><is><t>clipped</t></is></c>`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := replaceWorksheet(t, data, func(sheetXML string) string {
				if !strings.Contains(sheetXML, tc.marker) {
					t.Fatalf("insertion point absent in fixture: %s", sheetXML)
				}
				return strings.Replace(sheetXML, tc.marker, tc.insertion+tc.marker, 1)
			})
			got := readPreview(t, Excel, changed)
			if !strings.Contains(got.Notice, "截断") || len(got.Sheets) != 1 {
				t.Fatalf("preview limit not reported: notice = %q, sheets = %d", got.Notice, len(got.Sheets))
			}
			sheet := got.Sheets[0]
			if len(sheet.Rows) != 100 || len(sheet.Columns) != 26 || sheet.Columns[25] != "Z" || sheet.Rows[0][0] != "start" || sheet.Rows[99][25] != "last" {
				t.Fatalf("preview shape = %d rows, %d columns", len(sheet.Rows), len(sheet.Columns))
			}
		})
	}
}

func TestReadExcelVisibleSheetLimit(t *testing.T) {
	book := newTestWorkbook(t)
	for i := 2; i <= 10; i++ {
		addSheet(t, book, fmt.Sprintf("Sheet%d", i))
	}
	mustWorkbook(t, book.SetSheetVisible("Sheet2", false))
	mustWorkbook(t, book.SetSheetVisible("Sheet10", false, true))
	got := readPreview(t, Excel, workbookBytes(t, book))
	if len(got.Sheets) != 8 || got.Sheets[1].Name != "Sheet3" || got.Sheets[7].Name != "Sheet9" || strings.Contains(got.Notice, "截断") {
		t.Fatalf("eight visible sheets = %+v", got)
	}
	addSheet(t, book, "Extra")
	got = readPreview(t, Excel, workbookBytes(t, book))
	if len(got.Sheets) != 8 || got.Sheets[7].Name != "Sheet9" || !strings.Contains(got.Notice, "截断") {
		t.Fatalf("nine visible sheets = %+v", got)
	}
}

func TestReadExcelTextLimitAcrossSheets(t *testing.T) {
	book := newTestWorkbook(t)
	addSheet(t, book, "Sheet2")
	longCell := strings.Repeat("界", 10000)
	for i := 1; i <= 9; i++ {
		name, cell := "Sheet1", fmt.Sprintf("A%d", i)
		if i > 5 {
			name, cell = "Sheet2", fmt.Sprintf("A%d", i-5)
		}
		mustWorkbook(t, book.SetCellValue(name, cell, longCell))
	}
	got := readPreview(t, Excel, workbookBytes(t, book))
	if len(got.Sheets) != 2 || !strings.Contains(got.Notice, "256 KiB") || !strings.Contains(got.Notice, "截断") {
		t.Fatalf("sheets = %d, notice = %q", len(got.Sheets), got.Notice)
	}
	var size int
	for _, sheet := range got.Sheets {
		size += len(sheet.Name) + len(sheet.Columns)
		for _, row := range sheet.Rows {
			for _, value := range row {
				if !utf8.ValidString(value) {
					t.Fatal("truncation split a UTF-8 character")
				}
				size += len(value)
			}
		}
	}
	last := got.Sheets[1].Rows[3][0]
	if size > textLimit || size < textLimit-100 || len(last) == 0 || len(last) >= len(longCell) || !strings.HasPrefix(longCell, last) {
		t.Fatalf("total preview bytes = %d, last cell bytes = %d", size, len(last))
	}
}

func newTestWorkbook(t *testing.T) *excelize.File {
	t.Helper()
	book := excelize.NewFile()
	t.Cleanup(func() { mustWorkbook(t, book.Close()) })
	return book
}

func workbookBytes(t *testing.T, book *excelize.File) []byte {
	t.Helper()
	data, err := book.WriteToBuffer()
	mustWorkbook(t, err)
	return data.Bytes()
}

func addSheet(t *testing.T, book *excelize.File, name string) {
	t.Helper()
	_, err := book.NewSheet(name)
	mustWorkbook(t, err)
}

func mustWorkbook(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func replaceWorksheet(t *testing.T, data []byte, change func(string) string) []byte {
	t.Helper()
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	mustWorkbook(t, err)
	var b bytes.Buffer
	w := zip.NewWriter(&b)
	for _, p := range archive.File {
		if p.Name != "xl/worksheets/sheet1.xml" {
			mustWorkbook(t, w.Copy(p))
			continue
		}
		r, err := p.Open()
		mustWorkbook(t, err)
		original, err := io.ReadAll(r)
		mustWorkbook(t, err)
		mustWorkbook(t, r.Close())
		part, err := w.CreateHeader(&zip.FileHeader{Name: p.Name, Method: zip.Store})
		mustWorkbook(t, err)
		_, err = io.WriteString(part, change(string(original)))
		mustWorkbook(t, err)
	}
	mustWorkbook(t, w.Close())
	return b.Bytes()
}

func checkSheet(t *testing.T, got Sheet, name string, columns []string, rows [][]string) {
	t.Helper()
	if got.Name != name || !slices.Equal(got.Columns, columns) || !slices.EqualFunc(got.Rows, rows, slices.Equal[[]string]) {
		t.Fatalf("sheet = %+v, want %q, columns %v, rows %v", got, name, columns, rows)
	}
}
