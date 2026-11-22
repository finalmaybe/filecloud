package dav

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mingming-cn/filecloud/internal/auth"
	"github.com/mingming-cn/filecloud/internal/library"
	"github.com/mingming-cn/filecloud/internal/object"
	"github.com/mingming-cn/filecloud/internal/storage"
)

func (q *request) activeLock(l storage.DAVLock) element {
	depth := "0"
	if l.Deep {
		depth = "infinity"
	}
	v := dav("activelock", "", dav("locktype", "", dav("write", "")), dav("lockscope", "", dav("exclusive", "")), dav("depth", depth), dav("timeout", "Second-"+strconv.FormatInt(max(0, l.Expires-time.Now().Unix()), 10)), dav("lockroot", "", dav("href", q.href(l.Path, false))))
	if l.Credential == q.cred.ID {
		v.Children = append(v.Children, dav("locktoken", "", dav("href", l.Token)))
	}
	return v
}
func (q *request) lock(w http.ResponseWriter, e object.DirectoryEntry, d storage.DAVChange, p proofs) error {
	ctx := q.r.Context()
	now := time.Now()
	if q.r.Method == "UNLOCK" {
		token, tail, ok := delimited(strings.TrimSpace(q.r.Header.Get("Lock-Token")), '<', '>')
		if !ok || tail != "" {
			return failure(400)
		}
		for _, l := range q.locks {
			if l.Token == token && l.Path == q.src && l.Credential == q.cred.ID {
				d.Required[token] = true
				d.Drop = []string{token}
				if err := q.t.ChangeDAVLock(ctx, d); err != nil {
					return err
				}
				w.WriteHeader(204)
				return nil
			}
		}
		return failure(409)
	}
	seconds := int64(600)
	if raw := q.r.Header.Get("Timeout"); raw != "" {
		accepted := false
		for item := range strings.SplitSeq(raw, ",") {
			v := strings.TrimSpace(item)
			if v == "Infinite" {
				seconds = 3600
				accepted = true
				break
			}
			if n, err := strconv.ParseInt(strings.TrimPrefix(v, "Second-"), 10, 64); strings.HasPrefix(v, "Second-") && err == nil && n > 0 {
				seconds = min(n, 3600)
				accepted = true
				break
			}
		}
		if !accepted {
			return failure(400)
		}
	}
	body, err := readXML(w, q.r)
	if err != nil {
		return err
	}
	var l storage.DAVLock
	created := false
	if body.XMLName.Local == "" {
		for _, active := range q.locks {
			if active.Path == q.src && p.tokens[active.Token] {
				l = active
				break
			}
		}
		if l.Token == "" {
			return failure(412)
		}
	} else {
		if !body.named("lockinfo") {
			return failure(400)
		}
		var excl, write bool
		for _, kid := range body.Children {
			if kid.named("lockscope") {
				excl = len(kid.Children) == 1 && kid.has("exclusive")
			}
			if kid.named("locktype") {
				write = len(kid.Children) == 1 && kid.has("write")
			}
		}
		if !excl || !write {
			return failure(403)
		}
		depth := q.r.Header.Get("Depth")
		if depth != "0" && depth != "" && depth != "infinity" {
			return failure(400)
		}
		id, err := auth.NewUserID(nil)
		if err != nil {
			return err
		}
		l = storage.DAVLock{Token: "urn:uuid:" + id, Path: q.src, Credential: q.cred.ID, Deep: depth != "0"}
		for _, active := range q.locks {
			if active.Covers(q.src) || l.Covers(active.Path) || e.Type == "" && library.DAVTouched(active, q.src, true, false) && !p.tokens[active.Token] {
				return storage.ErrDAVLocked
			}
		}
		created = e.Type == ""
	}
	l.Expires = now.Unix() + seconds
	d.Set = &l
	code := 200
	if created {
		file, err := q.t.Upload(ctx, strings.NewReader(""), 0)
		if err != nil {
			return err
		}
		if err = q.t.Mutate(ctx, "LOCK", q.src, "", file, false, d); err != nil {
			return err
		}
		code = 201
	} else if err = q.t.ChangeDAVLock(ctx, d); err != nil {
		return err
	}
	w.Header().Set("Lock-Token", "<"+l.Token+">")
	w.Header().Set("Timeout", "Second-"+strconv.FormatInt(seconds, 10))
	return writeXML(w, code, dav("prop", "", dav("lockdiscovery", "", q.activeLock(l))))
}
