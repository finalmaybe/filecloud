package officepreview

import (
	"archive/zip"
	"encoding/xml"
	"strings"
)

const (
	wordXMLLimit        = 8 << 20
	wordPart            = "word/document.xml"
	wordNamespace       = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	strictWordNamespace = "http://purl.oclc.org/ooxml/wordprocessingml/main"
)

func readWord(archive *zip.Reader) Document {
	var part *zip.File
	for _, entry := range archive.File {
		if entry.Name == wordPart {
			if part != nil {
				return Document{Notice: unsupportedNotice}
			}
			part = entry
		}
	}
	if part == nil || part.Flags&1 != 0 {
		return Document{Notice: unsupportedNotice}
	}
	if part.UncompressedSize64 > wordXMLLimit {
		return Document{Notice: unzipLimitNotice}
	}
	r, err := part.Open()
	if err != nil {
		return Document{Notice: unsupportedNotice}
	}
	defer r.Close()

	var namespace string
	var seenBody, inBody, seenParagraph bool
	var paragraphs, textDepth int
	var text strings.Builder
	budget := textBudget{remaining: textLimit}
	appendText := func(value string) { text.WriteString(budget.take(value)) }
	valid := walkXML(r, func(token xml.Token, depth int) bool {
		switch token := token.(type) {
		case xml.StartElement:
			if depth == 1 {
				namespace = token.Name.Space
				return token.Name.Local == "document" && (namespace == wordNamespace || namespace == strictWordNamespace)
			}
			if token.Name.Space != namespace {
				return true
			}
			if depth == 2 && token.Name.Local == "body" {
				if seenBody {
					return false
				}
				seenBody, inBody = true, true
			}
			if !inBody {
				return true
			}
			if token.Name.Local == "p" {
				if seenParagraph {
					appendText("\n")
				}
				seenParagraph = true
				paragraphs++
			}
			if paragraphs > 0 {
				switch token.Name.Local {
				case "t":
					textDepth = depth
				case "br", "cr":
					appendText("\n")
				case "tab":
					appendText("\t")
				}
			}
		case xml.EndElement:
			if depth == textDepth {
				textDepth = 0
			}
			if token.Name.Space == namespace && inBody {
				if token.Name.Local == "p" && paragraphs > 0 {
					paragraphs--
				}
				if depth == 2 {
					inBody = false
				}
			}
		case xml.CharData:
			if depth == textDepth && textDepth > 0 && !budget.truncated {
				appendText(string(token))
			}
		}
		return true
	})
	if !valid || !seenBody {
		return Document{Notice: unsupportedNotice}
	}
	if budget.truncated {
		return Document{Text: text.String(), Notice: "预览内容已截断，最多显示 256 KiB。可下载原文件查看全文。"}
	}
	if strings.TrimSpace(text.String()) == "" {
		return Document{Notice: "此文档没有可预览的正文文字。"}
	}
	return Document{Text: text.String()}
}
