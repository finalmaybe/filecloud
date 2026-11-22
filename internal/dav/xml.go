package dav

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/mingming-cn/filecloud/internal/object"
)

type element struct {
	XMLName  xml.Name
	Text     string    `xml:",chardata"`
	Children []element `xml:",any"`
}

func dav(name, text string, kids ...element) element {
	return element{xml.Name{Space: "DAV:", Local: name}, text, kids}
}
func (e element) named(name string) bool { return e.XMLName == (xml.Name{Space: "DAV:", Local: name}) }
func (e element) has(name string) bool {
	for _, kid := range e.Children {
		if kid.named(name) {
			return true
		}
	}
	return false
}
func readXML(w http.ResponseWriter, r *http.Request) (element, error) {
	var root element
	dec := xml.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	var stack []*element
	nodes := 0
	for {
		token, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return root, nil
		}
		if err != nil {
			var size *http.MaxBytesError
			if errors.As(err, &size) {
				return root, size
			}
			return root, failure(400)
		}
		switch t := token.(type) {
		case xml.StartElement:
			nodes++
			if len(stack) >= 32 || nodes > 512 {
				return root, failure(413)
			}
			n := element{XMLName: t.Name}
			if len(stack) == 0 {
				if nodes != 1 {
					return root, failure(400)
				}
				root = n
				stack = append(stack, &root)
			} else {
				p := stack[len(stack)-1]
				p.Children = append(p.Children, n)
				stack = append(stack, &p.Children[len(p.Children)-1])
			}
		case xml.EndElement:
			stack = stack[:len(stack)-1]
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].Text += string(t)
			} else if strings.TrimSpace(string(t)) != "" {
				return root, failure(400)
			}
		case xml.Directive:
			return root, failure(400)
		}
	}
}
func writeXML(w http.ResponseWriter, status int, e element) error {
	data, err := xml.Marshal(e)
	if err != nil {
		return err
	}
	if len(data) > 8<<20 {
		return failure(507)
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write(data)
	return nil
}
func propstat(props []element, code int) element {
	return dav("propstat", "", dav("prop", "", props...), dav("status", "HTTP/1.1 "+strconv.Itoa(code)+" "+http.StatusText(code)))
}

var propertyNames = []string{"displayname", "resourcetype", "getetag", "getlastmodified", "getcontentlength", "getcontenttype", "supportedlock", "lockdiscovery"}

func (q *request) propfind(w http.ResponseWriter) error {
	depth := q.r.Header.Get("Depth")
	if depth == "" || depth == "infinity" {
		return writeXML(w, 403, dav("error", "", dav("propfind-finite-depth", "")))
	}
	if depth != "0" && depth != "1" {
		return failure(400)
	}
	body, err := readXML(w, q.r)
	if err != nil {
		return err
	}
	mode := "allprop"
	var requested []element
	if body.XMLName.Local != "" {
		if !body.named("propfind") || len(body.Children) != 1 {
			return failure(400)
		}
		selector := body.Children[0]
		switch {
		case selector.named("prop"), selector.named("allprop"), selector.named("propname"):
			mode = selector.XMLName.Local
			requested = selector.Children
		default:
			return failure(400)
		}
	}
	if mode != "prop" {
		for _, n := range propertyNames {
			requested = append(requested, dav(n, ""))
		}
	}
	if len(requested) > 64 {
		return failure(413)
	}
	ctx, done, err := q.h.Catalog.AdmitDAVListing(q.r.Context(), q.cred.Owner)
	if err != nil {
		return err
	}
	defer done()
	q.r = q.r.WithContext(ctx)
	q.locks, err = q.h.Store.DAVLocks(ctx, q.cred.Owner, q.cred.Library, time.Now())
	if err != nil {
		return err
	}
	top := object.DirectoryEntry{Type: "Directory", Name: "WebDAV"}
	if q.src != "" {
		top, err = q.t.Entry(q.r.Context(), q.src)
		if err != nil {
			return err
		}
		if top.Type == "" {
			return failure(404)
		}
		if q.src == "." {
			top.Name = q.t.Library.Name
		}
	}
	var out bytes.Buffer
	enc := xml.NewEncoder(&out)
	container := xml.StartElement{Name: dav("multistatus", "").XMLName}
	if err := enc.EncodeToken(container); err != nil {
		return err
	}
	emit := func(p string, e object.DirectoryEntry) error {
		row, err := q.properties(p, e, mode, requested)
		if err != nil {
			return err
		}
		if err = enc.Encode(row); err != nil {
			return err
		}
		if out.Len() > 8<<20 {
			return failure(507)
		}
		return nil
	}
	if err = emit(q.src, top); err != nil {
		return err
	}
	if depth == "1" && top.Type == "Directory" {
		if q.src == "" {
			e, err := q.t.Entry(q.r.Context(), ".")
			if err != nil {
				return err
			}
			e.Name = q.t.Library.Name
			if err = emit(".", e); err != nil {
				return err
			}
		} else {
			dir, err := q.t.Directory(q.r.Context(), top.ID)
			if err != nil {
				return err
			}
			if len(dir.Entries) > 5000 {
				return failure(507)
			}
			for _, e := range dir.Entries {
				if err := emit(path.Join(q.src, e.Name), e); err != nil {
					return err
				}
			}
		}
	}
	if err = enc.EncodeToken(container.End()); err != nil {
		return err
	}
	if err = enc.Flush(); err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(207)
	_, _ = out.WriteTo(w)
	return nil
}
func (q *request) properties(p string, e object.DirectoryEntry, mode string, requested []element) (element, error) {
	var good, bad []element
	for _, r := range requested {
		v := element{XMLName: r.XMLName}
		known := v.XMLName.Space == "DAV:"
		if known {
			switch v.XMLName.Local {
			case "displayname":
				v.Text = e.Name
			case "resourcetype":
				if e.Type == "Directory" {
					v.Children = []element{dav("collection", "")}
				}
			case "getetag":
				v.Text = etag(e)
			case "getlastmodified":
				t, err := time.Parse(time.RFC3339, e.ModifiedAt)
				if err == nil {
					v.Text = t.UTC().Format(http.TimeFormat)
				} else {
					known = false
				}
			case "getcontentlength":
				if e.Type == "File" {
					if mode != "propname" {
						f, err := q.t.File(q.r.Context(), e)
						if err != nil {
							return element{}, err
						}
						v.Text = strconv.FormatInt(f.Size, 10)
					}
				} else {
					known = false
				}
			case "getcontenttype":
				if e.Type == "File" {
					v.Text = "application/octet-stream"
				} else {
					known = false
				}
			case "supportedlock":
				if p != "" {
					v.Children = []element{dav("lockentry", "", dav("lockscope", "", dav("exclusive", "")), dav("locktype", "", dav("write", "")))}
				}
			case "lockdiscovery":
				if p != "" {
					for _, l := range q.locks {
						if l.Covers(p) {
							v.Children = append(v.Children, q.activeLock(l))
						}
					}
				}
			default:
				known = false
			}
		}
		if known {
			if mode == "propname" {
				v.Text = ""
				v.Children = nil
			}
			good = append(good, v)
		} else if mode == "prop" {
			bad = append(bad, v)
		}
	}
	resp := dav("response", "", dav("href", q.href(p, e.Type == "Directory")))
	if len(good) > 0 || len(bad) == 0 {
		resp.Children = append(resp.Children, propstat(good, 200))
	}
	if len(bad) > 0 {
		resp.Children = append(resp.Children, propstat(bad, 404))
	}
	return resp, nil
}
func (q *request) proppatch(w http.ResponseWriter) error {
	e, err := readXML(w, q.r)
	if err != nil {
		return err
	}
	if !e.named("propertyupdate") {
		return failure(400)
	}
	var props []element
	for _, op := range e.Children {
		if !(op.named("set") || op.named("remove")) || len(op.Children) != 1 || !op.Children[0].named("prop") {
			return failure(400)
		}
		for _, p := range op.Children[0].Children {
			props = append(props, element{XMLName: p.XMLName})
		}
	}
	if len(props) == 0 {
		return failure(400)
	}
	resp := dav("response", "", dav("href", q.href(q.src, false)), propstat(props[:1], 403))
	if len(props) > 1 {
		resp.Children = append(resp.Children, propstat(props[1:], 424))
	}
	return writeXML(w, 207, dav("multistatus", "", resp))
}
