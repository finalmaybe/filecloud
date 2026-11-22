package dav

import (
	"net/http"
	"strings"

	"github.com/mingming-cn/filecloud/internal/storage"
)

type proofs struct{ version, tokens map[string]bool }

func newProofs() proofs { return proofs{version: map[string]bool{}, tokens: map[string]bool{}} }

func (p proofs) locked(locks []storage.DAVLock, target string) bool {
	for _, l := range locks {
		if p.tokens[l.Token] && l.Covers(target) {
			return true
		}
	}
	return false
}
func (q *request) conditions() (proofs, error) {
	p := newProofs()
	e, err := q.t.Entry(q.r.Context(), q.src)
	if err != nil {
		return p, err
	}
	tag := etag(e)
	for _, name := range []string{"If-Match", "If-None-Match"} {
		raw := strings.Join(q.r.Header.Values(name), ",")
		if raw == "" {
			continue
		}
		positive := name == "If-Match"
		matched := false
		if strings.TrimSpace(raw) == "*" {
			matched = tag != ""
		} else {
			for x := range strings.SplitSeq(raw, ",") {
				x = strings.TrimSpace(x)
				if !positive {
					x = strings.TrimPrefix(x, "W/")
				}
				if tag != "" && x == tag {
					matched = true
					if positive {
						p.version[q.src] = true
					}
				}
			}
		}
		if matched != positive {
			return p, failure(412)
		}
	}
	return q.davConditions(p)
}

func (q *request) davConditions(p proofs) (proofs, error) {
	values := q.r.Header.Values("If")
	if len(values) == 0 {
		return p, nil
	}
	if len(values) != 1 || len(values[0]) > 8192 {
		return p, failure(400)
	}
	s := strings.TrimSpace(values[0])
	tagged := strings.HasPrefix(s, "<")
	if s == "" {
		return p, failure(400)
	}
	for groups := 0; s != ""; groups++ {
		if groups >= 32 {
			return p, failure(400)
		}
		target := q.src
		if tagged {
			value, rest, ok := delimited(s, '<', '>')
			if !ok {
				return p, failure(400)
			}
			var err error
			target, err = q.reference(value)
			if err != nil {
				return p, err
			}
			if target == "" {
				return p, failure(403)
			}
			s = rest
		}
		entry, err := q.t.Entry(q.r.Context(), target)
		if err != nil {
			return p, err
		}
		if !strings.HasPrefix(s, "(") {
			return p, failure(400)
		}
		anyTrue := false
		for strings.HasPrefix(s, "(") {
			s = strings.TrimSpace(s[1:])
			match := true
			count := 0
			strong := false
			for !strings.HasPrefix(s, ")") {
				neg := strings.HasPrefix(s, "Not ") || strings.HasPrefix(s, "Not\t")
				if neg {
					s = strings.TrimSpace(s[3:])
				}
				var test bool
				switch {
				case strings.HasPrefix(s, "<"):
					v, rest, ok := delimited(s, '<', '>')
					if !ok {
						return p, failure(400)
					}
					s = rest
					for _, l := range q.locks {
						if l.Token == v && l.Credential == q.cred.ID {
							// RFC 4918 §10.4.1: submitting a token and matching it on the tagged URI are distinct.
							p.tokens[v] = true
							test = l.Covers(target)
							break
						}
					}
				case strings.HasPrefix(s, "["):
					v, rest, ok := delimited(s, '[', ']')
					if !ok {
						return p, failure(400)
					}
					s = rest
					test = v == etag(entry) && entry.Type != ""
					if test && !neg {
						strong = true
					}
				default:
					return p, failure(400)
				}
				count++
				if neg {
					test = !test
				}
				match = match && test
			}
			if count == 0 {
				return p, failure(400)
			}
			s = strings.TrimSpace(s[1:])
			if match {
				anyTrue = true
				if strong {
					p.version[target] = true
				}
			}
		}
		if !anyTrue {
			return p, failure(http.StatusPreconditionFailed)
		}
		if !tagged && s != "" {
			return p, failure(400)
		}
	}
	return p, nil
}
func delimited(s string, start, end byte) (string, string, bool) {
	if len(s) == 0 || s[0] != start {
		return "", "", false
	}
	v, tail, ok := strings.Cut(s[1:], string(end))
	return v, strings.TrimSpace(tail), ok && v != ""
}
