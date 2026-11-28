// Package officepreview reads bounded, static previews of Office documents.
package officepreview

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"path"
	"strings"
	"unicode/utf8"
)

const (
	Word  = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	Excel = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"

	inputLimit    = 12 << 20
	zipPartLimit  = 2048
	textLimit     = 256 << 10
	xmlDepthLimit = 128

	unsupportedNotice = "此文件可能已损坏、加密或格式不受支持，暂不支持预览。可下载原文件查看。"
	inputLimitNotice  = "文件超过 12 MiB，暂不支持预览。可下载原文件查看。"
	unzipLimitNotice  = "文件解包后超过预览限制，暂不支持预览。可下载原文件查看。"
)

type Document struct {
	Text, Notice string
	Sheets       []Sheet
}

type Sheet struct {
	Name    string
	Columns []string
	Rows    [][]string
}

// Media reports the supported Office MIME type for name, or an empty string.
func Media(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".docx":
		return Word
	case ".xlsx":
		return Excel
	default:
		return ""
	}
}

// Read returns a static preview. A negative size denotes an unknown size.
// Only failures reading r are returned as errors; unpreviewable files have a Notice.
func Read(r io.Reader, media string, size int64) (Document, error) {
	if media != Word && media != Excel {
		return Document{Notice: unsupportedNotice}, nil
	}
	if size > inputLimit {
		return Document{Notice: inputLimitNotice}, nil
	}
	data, err := io.ReadAll(io.LimitReader(r, inputLimit+1))
	if err != nil {
		return Document{}, err
	}
	if len(data) > inputLimit {
		return Document{Notice: inputLimitNotice}, nil
	}
	if len(data) == 0 {
		return Document{Notice: "这是一个空文件。"}, nil
	}
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return Document{Notice: unsupportedNotice}, nil
	}
	if len(archive.File) > zipPartLimit {
		return Document{Notice: unzipLimitNotice}, nil
	}
	if media == Word {
		return readWord(archive), nil
	}
	return readExcel(data, archive), nil
}

// walkXML checks the entire XML document, even when the preview is already full.
func walkXML(r io.Reader, visit func(xml.Token, int) bool) bool {
	d := xml.NewDecoder(r)
	depth := 0
	seenRoot := false
	for {
		token, err := d.Token()
		if err != nil {
			return err == io.EOF && seenRoot && depth == 0
		}
		switch token := token.(type) {
		case xml.StartElement:
			if depth == 0 {
				if seenRoot {
					return false
				}
				seenRoot = true
			}
			depth++
			if depth > xmlDepthLimit {
				return false
			}
		case xml.CharData:
			if depth == 0 && strings.TrimSpace(strings.TrimPrefix(string(token), "\ufeff")) != "" {
				return false
			}
		}
		if !visit(token, depth) {
			return false
		}
		if _, ok := token.(xml.EndElement); ok {
			depth--
		}
	}
}

type textBudget struct {
	remaining int
	truncated bool
}

func (b *textBudget) take(text string) string {
	if len(text) > b.remaining {
		b.truncated = true
		end := b.remaining
		for end > 0 && !utf8.RuneStart(text[end]) {
			end--
		}
		b.remaining = 0
		return strings.Clone(text[:end])
	}
	b.remaining -= len(text)
	return text
}
