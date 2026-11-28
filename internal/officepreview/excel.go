package officepreview

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"path"
	"strings"

	"github.com/xuri/excelize/v2"
)

const (
	excelUnzipLimit = 32 << 20
	sheetLimit      = 8
	rowLimit        = 100
	columnLimit     = 26
)

func readExcel(data []byte, archive *zip.Reader) Document {
	if notice := checkExcelArchive(archive); notice != "" {
		return Document{Notice: notice}
	}
	// Keep the capped archive in memory, including worksheets and shared strings.
	book, err := excelize.OpenReader(bytes.NewReader(data), excelize.Options{
		UnzipSizeLimit: excelUnzipLimit, UnzipXMLSizeLimit: excelUnzipLimit,
	})
	if book != nil {
		defer book.Close()
	}
	if err != nil {
		return Document{Notice: unsupportedNotice}
	}
	names := book.GetSheetList()
	if len(names) == 0 {
		return Document{Notice: unsupportedNotice}
	}
	var doc Document
	var truncated, hasCells bool
	remaining := textLimit
	for _, name := range names {
		visible, err := book.GetSheetVisible(name)
		if err != nil {
			return Document{Notice: unsupportedNotice}
		}
		if !visible {
			continue
		}
		if len(doc.Sheets) == sheetLimit {
			truncated = true
			break
		}
		doc.Sheets = append(doc.Sheets, Sheet{Name: name})
		remaining -= len(name) + columnLimit // Reserve sheet names and single-letter column headings.
	}
	budget := textBudget{remaining: remaining}
	for i := range doc.Sheets {
		sheet := &doc.Sheets[i]
		cut, err := readSheet(book, sheet, &budget)
		if err != nil {
			return Document{Notice: unsupportedNotice}
		}
		truncated = truncated || cut
		hasCells = hasCells || len(sheet.Rows) > 0
	}
	switch {
	case truncated || budget.truncated:
		doc.Notice = "预览内容已截断：最多显示前 8 个可见工作表，每表前 100 行、26 列，文本最多 256 KiB。可下载原文件查看完整内容。"
	case len(doc.Sheets) == 0:
		doc.Notice = "此文件没有可预览的可见工作表。"
	case !hasCells:
		doc.Notice = "此文件没有可预览的单元格内容。"
	}
	return doc
}

func readSheet(book *excelize.File, sheet *Sheet, budget *textBudget) (bool, error) {
	rows, err := book.Rows(sheet.Name)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	var truncated bool
	var width, usedRows int
	for rows.Next() {
		// Next also yields gaps before the next physical row. Counting those
		// steps retains the sheet's coordinates and caps the scan at row 101.
		if len(sheet.Rows) == rowLimit {
			truncated = true
			break
		}
		// Columns applies cell formats and reads stored formula results without calculation.
		values, err := rows.Columns()
		if err != nil {
			return false, err
		}
		if len(values) > columnLimit {
			values = values[:columnLimit]
			truncated = true
		}
		var row []string
		if len(values) > 0 {
			width = max(width, len(values))
			usedRows = len(sheet.Rows) + 1
			row = make([]string, len(values))
			for i, value := range values {
				row[i] = budget.take(value)
			}
		}
		sheet.Rows = append(sheet.Rows, row)
	}
	if err := rows.Error(); err != nil {
		return false, err
	}
	if usedRows == 0 {
		sheet.Rows = nil
		return truncated, nil
	}
	sheet.Rows = sheet.Rows[:usedRows]
	for i, row := range sheet.Rows {
		if len(row) < width {
			sheet.Rows[i] = append(row, make([]string, width-len(row))...)
		}
	}
	for i := range width {
		sheet.Columns = append(sheet.Columns, string(rune('A'+i)))
	}
	return truncated, nil
}

// Excelize can silently stop its iterator on invalid XML or ignore a ZIP
// checksum error. Validate the XML used for cell values, not unrelated artwork.
func checkExcelArchive(archive *zip.Reader) string {
	var size uint64
	names := make(map[string]bool, len(archive.File))
	for _, part := range archive.File {
		if part.UncompressedSize64 > excelUnzipLimit-size {
			return unzipLimitNotice
		}
		size += part.UncompressedSize64
		name := strings.ToLower(part.Name)
		if part.Flags&1 != 0 || names[name] || strings.ContainsRune(name, '\\') {
			return unsupportedNotice
		}
		names[name] = true
	}
	for _, part := range archive.File {
		if !validExcelPart(part) {
			return unsupportedNotice
		}
	}
	return ""
}

func validExcelPart(part *zip.File) bool {
	name := strings.ToLower(part.Name)
	worksheet := path.Dir(name) == "xl/worksheets" && path.Ext(name) == ".xml"
	if !worksheet && name != "xl/workbook.xml" && name != "xl/_rels/workbook.xml.rels" && name != "xl/sharedstrings.xml" && name != "xl/styles.xml" {
		return true
	}
	r, err := part.Open()
	if err != nil {
		return false
	}
	defer r.Close()
	var root, metadataElement string
	var metadataCount int
	var sheetData bool
	valid := walkXML(r, func(token xml.Token, depth int) bool {
		start, ok := token.(xml.StartElement)
		if !ok {
			return true
		}
		if depth == 1 {
			root = start.Name.Local
			switch root {
			case "workbook":
				metadataElement = "sheet"
			case "Relationships":
				metadataElement = "Relationship"
			}
		} else if start.Name.Local == metadataElement {
			// The library matches workbook sheet records to relationships in a nested loop.
			metadataCount++
			if metadataCount > zipPartLimit {
				return false
			}
		}
		if depth == 2 && start.Name.Local == "sheetData" {
			sheetData = true
		}
		return true
	})
	return valid && (!worksheet || (root == "worksheet" && sheetData))
}
