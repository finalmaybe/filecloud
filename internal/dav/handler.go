// Package dav exposes an owner's current published tree as a writable network drive.
package dav

import (
	"errors"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/mingming-cn/filecloud/internal/auth"
	"github.com/mingming-cn/filecloud/internal/library"
	"github.com/mingming-cn/filecloud/internal/object"
	"github.com/mingming-cn/filecloud/internal/storage"
	"golang.org/x/text/unicode/norm"
)

const methods = "OPTIONS, PROPFIND, GET, HEAD, PUT, MKCOL, COPY, MOVE, DELETE, LOCK, UNLOCK, PROPPATCH"

type Handler struct {
	Origin  *auth.BrowserOrigin
	Store   *storage.Store
	Catalog *library.Catalog
}
type request struct {
	h        *Handler
	r        *http.Request
	cred     storage.DAVCredential
	t        library.DAVTree
	src, dst string
	locks    []storage.DAVLock
}
type failure int

func (f failure) Error() string { return http.StatusText(int(f)) }
func status(err error) int {
	var f failure
	var size *http.MaxBytesError
	switch {
	case errors.As(err, &f):
		return int(f)
	case errors.As(err, &size), errors.Is(err, object.ErrPayloadTooLarge):
		return 413
	case errors.Is(err, io.ErrUnexpectedEOF):
		return 400
	case errors.Is(err, storage.ErrLibraryNotFound):
		return 404
	case errors.Is(err, storage.ErrSessionNotFound):
		return 401
	case errors.Is(err, storage.ErrHeadConflict), errors.Is(err, library.ErrDAVConflict):
		return 409
	case errors.Is(err, storage.ErrDAVLocked):
		return 423
	case errors.Is(err, storage.ErrDAVCondition):
		return 412
	case errors.Is(err, storage.ErrUploadUnavailable):
		return 507
	case errors.Is(err, storage.ErrUploadRateLimited), errors.Is(err, library.ErrHistoryBusy):
		return 429
	default:
		return 503
	}
}
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if err := h.serve(w, r); err != nil {
		code := status(err)
		if code == 401 {
			w.Header().Set("WWW-Authenticate", `Basic realm="Filecloud WebDAV", charset="UTF-8"`)
		}
		if code == 429 {
			w.Header().Set("Retry-After", "1")
		}
		http.Error(w, http.StatusText(code), code)
	}
}
func (h *Handler) serve(w http.ResponseWriter, r *http.Request) error {
	if !h.Origin.MatchesHost(r.Host) {
		return failure(421)
	}
	if origins := r.Header.Values("Origin"); len(origins) > 0 && (len(origins) != 1 || !h.Origin.MatchesOrigin(origins[0])) {
		return failure(403)
	}
	ctx := r.Context()
	enabled, err := h.Store.WebDAVEnabled(ctx)
	if err != nil {
		return err
	}
	if !enabled {
		return failure(http.StatusNotFound)
	}
	id, secret, ok := r.BasicAuth()
	if !ok || len(r.Header.Values("Authorization")) != 1 {
		return failure(401)
	}
	cred, err := h.Store.AuthenticateDAV(ctx, id, secret, time.Now())
	if err != nil {
		return err
	}
	q := request{h: h, r: r, cred: cred}
	q.src, err = q.resource(r.URL)
	if err != nil {
		return err
	}
	done := h.Store.UseLibrary(cred.Owner, cred.Library)
	defer done()
	q.t, err = h.Catalog.DAVTree(ctx, cred.Owner, cred.Library)
	if err != nil {
		return err
	}
	if r.Method == "OPTIONS" {
		if q.src == "" {
			w.Header().Set("Allow", "OPTIONS, PROPFIND")
			w.Header().Set("DAV", "1")
		} else {
			w.Header().Set("Allow", methods)
			w.Header().Set("DAV", "1, 2")
		}
		w.Header().Set("MS-Author-Via", "DAV")
		w.WriteHeader(200)
		return nil
	}
	if (r.Method == "GET" || r.Method == "HEAD" || r.Method == "PROPFIND") && len(r.Header.Values("If")) != 0 {
		q.locks, err = h.Store.DAVLocks(ctx, cred.Owner, cred.Library, time.Now())
		if err != nil {
			return err
		}
		if _, err = q.davConditions(newProofs()); err != nil {
			return err
		}
	}
	if r.Method == "PROPFIND" {
		return q.propfind(w)
	}
	if q.src == "" {
		return failure(405)
	}
	switch r.Method {
	case "GET", "HEAD":
		e, err := q.t.Entry(ctx, q.src)
		if err != nil {
			return err
		}
		if e.Type == "" {
			return failure(404)
		}
		if e.Type != "File" {
			return failure(405)
		}
		f, err := q.t.File(ctx, e)
		if err != nil {
			return err
		}
		mtime, _ := time.Parse(time.RFC3339, e.ModifiedAt)
		w.Header().Set("ETag", etag(e))
		w.Header().Set("Content-Type", "application/octet-stream")
		http.ServeContent(w, r, path.Base(q.src), mtime, f.Content(ctx))
		return nil
	case "LOCK", "UNLOCK", "MKCOL", "DELETE", "PUT", "COPY", "MOVE", "PROPPATCH":
		if q.src == "." && r.Method != "LOCK" && r.Method != "UNLOCK" && r.Method != "PROPPATCH" {
			return failure(403)
		}
		return q.change(w)
	default:
		w.Header().Set("Allow", methods)
		return failure(405)
	}
}

// The empty path denotes /dav/; a dot denotes the mounted library root.
func (q *request) resource(u *url.URL) (string, error) {
	if u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Opaque != "" {
		return "", failure(400)
	}
	if u.Host != "" || u.Scheme != "" {
		if !q.h.Origin.MatchesOrigin(u.Scheme + "://" + u.Host) {
			return "", failure(403)
		}
	}
	escaped := u.EscapedPath()
	if escaped == "/dav/" || escaped == "/dav" {
		return "", nil
	}
	rest, ok := strings.CutPrefix(escaped, "/dav/")
	if !ok {
		return "", failure(403)
	}
	if strings.HasSuffix(rest, "//") {
		return "", failure(400)
	}
	parts := strings.Split(strings.TrimSuffix(rest, "/"), "/")
	if parts[0] != q.cred.Library {
		return "", failure(404)
	}
	if len(parts) == 1 {
		return ".", nil
	}
	for i := 1; i < len(parts); i++ {
		p, err := url.PathUnescape(parts[i])
		if err != nil || strings.Contains(p, "/") || p == "." || p == ".." {
			return "", failure(400)
		}
		parts[i] = norm.NFC.String(p)
	}
	p := strings.Join(parts[1:], "/")
	if !object.ValidPath(p) {
		return "", failure(400)
	}
	return p, nil
}
func (q *request) reference(raw string) (string, error) {
	u, err := url.ParseRequestURI(raw)
	if err != nil {
		return "", failure(400)
	}
	return q.resource(u)
}
func etag(e object.DirectoryEntry) string {
	if e.Type == "" {
		return ""
	}
	return `"` + object.ID([]byte(e.Type+"\x00"+e.ID+"\x00"+e.ModifiedAt)) + `"`
}

type timedBody struct {
	r    io.Reader
	ctrl *http.ResponseController
	end  time.Time
}

func (b timedBody) Read(p []byte) (int, error) {
	next := time.Now().Add(time.Minute)
	if next.After(b.end) {
		next = b.end
	}
	_ = b.ctrl.SetReadDeadline(next)
	return b.r.Read(p)
}

func (q *request) change(w http.ResponseWriter) error {
	r := q.r
	ctx := r.Context()
	method := r.Method
	move := method == "MOVE" || method == "COPY"
	var err error
	if move {
		q.dst, err = q.reference(r.Header.Get("Destination"))
		if err != nil {
			return err
		}
		if q.dst == "" || q.dst == "." {
			return failure(403)
		}
		switch r.Header.Get("Overwrite") {
		case "", "T", "F":
		default:
			return failure(400)
		}
		switch r.Header.Get("Depth") {
		case "", "infinity":
		case "0":
			if method == "MOVE" {
				return failure(400)
			}
		default:
			return failure(400)
		}
	}
	original, err := q.t.Entry(ctx, q.src)
	if err != nil {
		return err
	}
	var dest object.DirectoryEntry
	if move {
		dest, err = q.t.Entry(ctx, q.dst)
		if err != nil {
			return err
		}
	}
	var fileID string
	for attempt := 0; attempt < 2; attempt++ {
		q.locks, err = q.h.Store.DAVLocks(ctx, q.cred.Owner, q.cred.Library, time.Now())
		if err != nil {
			return err
		}
		proof, err := q.conditions()
		if err != nil {
			return err
		}
		d := storage.DAVChange{Credential: q.cred.ID, Checked: q.locks, Required: proof.tokens}
		if method == "LOCK" || method == "UNLOCK" {
			return q.lock(w, original, d, proof)
		}
		if method == "MKCOL" && original.Type != "" {
			return failure(405)
		}
		if (method == "DELETE" || move || method == "PROPPATCH") && original.Type == "" {
			return failure(404)
		}
		if method == "PUT" && original.Type == "Directory" {
			return failure(405)
		}
		if move && dest.Type != "" && r.Header.Get("Overwrite") == "F" {
			return failure(412)
		}
		target := q.src
		existing := original
		if move {
			target = q.dst
			existing = dest
		}
		removeSource := method == "DELETE" || method == "MOVE"
		replaceTarget := move && existing.Type != ""
		membership := existing.Type == "" || method == "DELETE" || replaceTarget
		for _, l := range q.locks {
			removed := removeSource && (l.Path == q.src || storage.DAVDescendant(l.Path, q.src))
			overwritten := replaceTarget && storage.DAVDescendant(l.Path, target)
			touch := library.DAVTouched(l, target, membership, replaceTarget || method == "DELETE") || method == "MOVE" && library.DAVTouched(l, q.src, true, true)
			if touch && !proof.tokens[l.Token] {
				return storage.ErrDAVLocked
			}
			if removed || overwritten {
				d.Drop = append(d.Drop, l.Token)
			}
		}
		if method == "PROPPATCH" {
			return q.proppatch(w)
		}
		if existing.Type != "" && !proof.version[target] && !proof.locked(q.locks, target) {
			return failure(428)
		}
		if method == "MKCOL" && r.ContentLength != 0 {
			return failure(415)
		}
		if method == "PUT" && attempt == 0 {
			if r.ContentLength > object.MaxFileSize {
				return failure(413)
			}
			ctrl := http.NewResponseController(w)
			defer ctrl.SetReadDeadline(time.Time{})
			fileID, err = q.t.Upload(ctx, timedBody{http.MaxBytesReader(w, r.Body, object.MaxFileSize), ctrl, time.Now().Add(time.Hour)}, r.ContentLength)
			if err != nil {
				return err
			}
		}
		err = q.t.Mutate(ctx, method, q.src, q.dst, fileID, r.Header.Get("Depth") == "0", d)
		if !errors.Is(err, storage.ErrHeadConflict) || attempt == 1 {
			if err != nil {
				return err
			}
			code := 204
			if existing.Type == "" {
				code = 201
			}
			w.WriteHeader(code)
			return nil
		}
		q.t, err = q.h.Catalog.DAVTree(ctx, q.cred.Owner, q.cred.Library)
		if err != nil {
			return err
		}
		changed, err := q.t.Entry(ctx, q.src)
		if err != nil {
			return err
		}
		if changed != original {
			return q.stale()
		}
		if move {
			changed, err = q.t.Entry(ctx, q.dst)
			if err != nil {
				return err
			}
			if changed != dest {
				return q.stale()
			}
		}
	}
	return storage.ErrHeadConflict
}
func (q *request) stale() error {
	if _, err := q.conditions(); err != nil {
		return err
	}
	return storage.ErrHeadConflict
}

func (q *request) href(p string, dir bool) string {
	raw := "/dav/"
	if p != "" {
		raw += q.cred.Library + "/"
	}
	if p != "" && p != "." {
		raw += p
		if dir {
			raw += "/"
		}
	}
	return (&url.URL{Path: raw}).EscapedPath()
}
