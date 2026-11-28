package webconsole

import (
	"io"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mingming-cn/filecloud/internal/library"
	"github.com/mingming-cn/filecloud/internal/officepreview"
)

const textPreviewLimit = 256 << 10

type filePreview struct {
	library.VersionTarget
	Name, Parent, Kind, Text, Notice string
	Sheets                           []officepreview.Sheet
}

func (c *console) previewFile(w http.ResponseWriter, r *http.Request, who identity) {
	q := r.URL.Query()
	target := versionTarget(r, q)
	file, err := c.Catalog.OpenSnapshotFile(r.Context(), who.user, target)
	if err != nil {
		c.problem(w, r, who, err)
		return
	}
	content := file.Content(r.Context())
	name := path.Base(target.Path)
	mode := q.Get("mode")
	if mode == "download" {
		servePreviewContent(w, r, name, "attachment", "application/octet-stream", content)
		return
	}
	media, err := previewType(name, content)
	if err != nil {
		c.problem(w, r, who, err)
		return
	}
	if mode == "inline" {
		if !strings.HasPrefix(media, "image/") && media != "application/pdf" {
			http.Error(w, "此文件不支持内嵌预览。", http.StatusUnsupportedMediaType)
			return
		}
		servePreviewContent(w, r, name, "inline", media, content)
		return
	}
	data := filePreview{VersionTarget: target, Name: name, Parent: path.Dir(target.Path)}
	switch media {
	case officepreview.Word, officepreview.Excel:
		doc, err := officepreview.Read(content, media, file.Size)
		if err != nil {
			c.problem(w, r, who, err)
			return
		}
		data.Text, data.Sheets, data.Notice = doc.Text, doc.Sheets, doc.Notice
		if doc.Text != "" {
			data.Kind = "word"
		}
	case "text/plain":
		prefix, err := io.ReadAll(io.LimitReader(content, textPreviewLimit))
		if err != nil {
			c.problem(w, r, who, err)
			return
		}
		truncated := file.Size > textPreviewLimit
		if text, ok := previewText(prefix, truncated); ok {
			data.Kind, data.Text = "text", text
			if truncated {
				data.Notice = "仅显示前 256 KiB，可下载原文件查看全文。"
			} else if file.Size == 0 {
				data.Notice = "这是一个空文件。"
			}
		} else {
			data.Notice = "此文件不是可预览的 UTF-8 文本。"
		}
	case "application/pdf":
		data.Kind = "pdf"
		w.Header().Set("Content-Security-Policy", w.Header().Get("Content-Security-Policy")+"; frame-src 'self'")
	case "":
		data.Notice = "暂不支持预览此文件，可下载原文件查看。"
	default:
		data.Kind = "image"
	}
	if mode == "stage" {
		c.renderTemplate(w, r, http.StatusOK, "viewer-stage", data)
		return
	}
	c.show(w, r, who, http.StatusOK, name+" · 文件预览", "file-preview", data, "")
}

func servePreviewContent(w http.ResponseWriter, r *http.Request, name, disposition, media string, content io.ReadSeeker) {
	w.Header().Set("Content-Type", media)
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": name}))
	if media == "application/pdf" {
		w.Header().Set("Content-Security-Policy", "default-src 'none'; object-src 'self'; frame-ancestors 'self'; base-uri 'none'; form-action 'none'")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
	}
	http.ServeContent(w, r, name, time.Time{}, content)
}

func previewType(name string, content *io.SectionReader) (string, error) {
	var media string
	switch strings.ToLower(path.Ext(name)) {
	case ".jpg", ".jpeg":
		media = "image/jpeg"
	case ".png":
		media = "image/png"
	case ".webp":
		media = "image/webp"
	case ".gif":
		media = "image/gif"
	case ".pdf":
		media = "application/pdf"
	case ".txt", ".md", ".log", ".csv", ".tsv", ".json", ".yaml", ".yml", ".toml", ".ini", ".conf",
		".html", ".htm", ".svg", ".xml", ".css", ".js", ".jsx", ".ts", ".tsx", ".go", ".py", ".rs",
		".sh", ".sql", ".c", ".h", ".cpp", ".hpp", ".java", ".rb", ".php":
		return "text/plain", nil
	default:
		return officepreview.Media(name), nil
	}
	var prefix [512]byte
	n, err := content.ReadAt(prefix[:], 0)
	if err != nil && err != io.EOF {
		return "", err
	}
	if http.DetectContentType(prefix[:n]) != media {
		return "", nil
	}
	return media, nil
}

func previewText(data []byte, truncated bool) (string, bool) {
	for offset := 0; offset < len(data); {
		r, n := utf8.DecodeRune(data[offset:])
		if r == utf8.RuneError && n == 1 {
			// A byte limit may split the final character of otherwise valid text.
			if truncated && !utf8.FullRune(data[offset:]) {
				return string(data[:offset]), true
			}
			return "", false
		}
		if r == 0 {
			return "", false
		}
		offset += n
	}
	return string(data), true
}
