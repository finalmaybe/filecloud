package storage

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"math/big"
	"strings"
	"time"
)

var davMigration = []string{
	`CREATE TABLE dav_credentials (id TEXT PRIMARY KEY, owner TEXT NOT NULL, library TEXT NOT NULL, name TEXT NOT NULL, secret BLOB NOT NULL, created INTEGER NOT NULL, expires INTEGER NOT NULL, revoked INTEGER NOT NULL DEFAULT 0, FOREIGN KEY(owner,library) REFERENCES libraries(owner_user_id,id) ON DELETE CASCADE)`,
	`CREATE INDEX dav_credentials_scope ON dav_credentials(owner,library)`,
	`CREATE TABLE dav_locks (token TEXT PRIMARY KEY, credential TEXT NOT NULL REFERENCES dav_credentials(id) ON DELETE CASCADE, path TEXT NOT NULL, deep INTEGER NOT NULL, expires INTEGER NOT NULL)`,
	`CREATE INDEX dav_locks_credential ON dav_locks(credential)`,
	`CREATE TRIGGER dav_revoke AFTER UPDATE OF revoked ON dav_credentials WHEN NEW.revoked=1 BEGIN DELETE FROM dav_locks WHERE credential=NEW.id; END`,
	`CREATE TRIGGER dav_reset AFTER UPDATE OF password_hash ON users BEGIN UPDATE dav_credentials SET revoked=1 WHERE owner=NEW.id AND revoked=0; END`,
}

var ErrDAVLocked = errors.New("resource locked")
var ErrDAVCondition = errors.New("DAV condition changed")

type DAVCredential struct {
	ID, Owner, Library, Name string
	Created, Expires         time.Time
}

func (s *Store) CreateDAVCredential(ctx context.Context, owner, lib, id, name string, now time.Time) (string, error) {
	secret, err := newDAVPassword()
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(secret))
	_, err = s.db.ExecContext(ctx, `INSERT INTO dav_credentials(id,owner,library,name,secret,created,expires) VALUES(?,?,?,?,?,?,?)`, id, owner, lib, name, hash[:], now.Unix(), now.AddDate(1, 0, 0).Unix())
	if err != nil {
		return "", err
	}
	return secret, nil
}

func newDAVPassword() (string, error) {
	const upper = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	const lower = "abcdefghijklmnopqrstuvwxyz"
	const digits = "0123456789"
	const alphabet = upper + lower + digits
	limit := big.NewInt(int64(len(alphabet)))
	var chars [26]byte
	for {
		for i := range chars {
			n, err := rand.Int(rand.Reader, limit)
			if err != nil {
				return "", err
			}
			chars[i] = alphabet[n.Int64()]
		}
		password := string(chars[:])
		if strings.ContainsAny(password, upper) && strings.ContainsAny(password, lower) && strings.ContainsAny(password, digits) {
			return password, nil
		}
	}
}

func (s *Store) DAVCredentials(ctx context.Context, owner, lib string) ([]DAVCredential, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,created,expires FROM dav_credentials WHERE owner=? AND library=? AND revoked=0 ORDER BY created DESC,id`, owner, lib)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []DAVCredential
	for rows.Next() {
		var c DAVCredential
		var a, b int64
		if err := rows.Scan(&c.ID, &c.Name, &a, &b); err != nil {
			return nil, err
		}
		c.Created, c.Expires = time.Unix(a, 0).UTC(), time.Unix(b, 0).UTC()
		result = append(result, c)
	}
	return result, rows.Err()
}
func (s *Store) AuthenticateDAV(ctx context.Context, id, secret string, now time.Time) (DAVCredential, error) {
	c := DAVCredential{ID: id}
	hash := sha256.Sum256([]byte(secret))
	err := s.db.QueryRowContext(ctx, `SELECT owner,library FROM dav_credentials WHERE id=? AND secret=? AND revoked=0 AND expires>?`, id, hash[:], now.Unix()).Scan(&c.Owner, &c.Library)
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrSessionNotFound
	}
	return c, err
}
func (s *Store) RevokeDAV(ctx context.Context, owner, lib, id string) error {
	r, err := s.db.ExecContext(ctx, `UPDATE dav_credentials SET revoked=1 WHERE owner=? AND library=? AND id=?`, owner, lib, id)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err == nil && n == 0 {
		return ErrLibraryNotFound
	}
	return err
}

type DAVLock struct {
	Token, Credential, Path string
	Deep                    bool
	Expires                 int64
}

func DAVDescendant(path, parent string) bool {
	return path != "." && (parent == "." || strings.HasPrefix(path, parent+"/"))
}
func (l DAVLock) Covers(path string) bool {
	return path == l.Path || l.Deep && DAVDescendant(path, l.Path)
}

type DAVChange struct {
	Credential string
	Checked    []DAVLock
	Required   map[string]bool
	Drop       []string
	Set        *DAVLock
}

func (s *Store) DAVLocks(ctx context.Context, owner, lib string, now time.Time) ([]DAVLock, error) {
	return davLocks(ctx, s.db, owner, lib, now)
}

type lockQuery interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func davLocks(ctx context.Context, q lockQuery, owner, lib string, now time.Time) ([]DAVLock, error) {
	rows, err := q.QueryContext(ctx, `SELECT l.token,l.credential,l.path,l.deep,l.expires FROM dav_locks l JOIN dav_credentials c ON c.id=l.credential WHERE c.owner=? AND c.library=? AND c.revoked=0 AND c.expires>? AND l.expires>?`, owner, lib, now.Unix(), now.Unix())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DAVLock
	for rows.Next() {
		var l DAVLock
		if err := rows.Scan(&l.Token, &l.Credential, &l.Path, &l.Deep, &l.Expires); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Locks and credentials can change without advancing Head. Check the observed lock set
// inside the same write transaction that makes a snapshot or a lock visible.
func (d DAVChange) apply(ctx context.Context, tx *sql.Tx, owner, lib string, now time.Time) error {
	if d.Credential != "" {
		var active bool
		err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM dav_credentials WHERE id=? AND owner=? AND library=? AND revoked=0 AND expires>?)`, d.Credential, owner, lib, now.Unix()).Scan(&active)
		if err != nil {
			return err
		}
		if !active {
			return ErrSessionNotFound
		}
	}
	locks, err := davLocks(ctx, tx, owner, lib, now)
	if err != nil {
		return err
	}
	checked := map[string]bool{}
	for _, l := range d.Checked {
		checked[l.Token] = true
	}
	live := map[string]bool{}
	for _, l := range locks {
		if !checked[l.Token] {
			// A DAV upload may have streamed its body while another request acquired a lock.
			if d.Credential != "" && d.Set == nil {
				return ErrHeadConflict
			}
			return ErrDAVLocked
		}
		live[l.Token] = true
	}
	for token := range d.Required {
		if !live[token] {
			return ErrDAVCondition
		}
	}
	for _, token := range d.Drop {
		if _, err := tx.ExecContext(ctx, `DELETE FROM dav_locks WHERE token=? AND credential=?`, token, d.Credential); err != nil {
			return err
		}
	}
	if l := d.Set; l != nil {
		if !live[l.Token] && len(locks) >= 128 {
			return ErrUploadRateLimited
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM dav_locks WHERE credential=? AND expires<=?`, d.Credential, now.Unix()); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO dav_locks(token,credential,path,deep,expires) VALUES(?,?,?,?,?) ON CONFLICT(token) DO UPDATE SET expires=excluded.expires`, l.Token, d.Credential, l.Path, l.Deep, l.Expires)
	}
	return err
}

func (s *Store) ApplyDAV(ctx context.Context, lib Library, d DAVChange, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	r, err := tx.ExecContext(ctx, `UPDATE libraries SET head_version=head_version WHERE owner_user_id=? AND id=? AND head_version=?`, lib.OwnerUserID, lib.ID, lib.HeadVersion)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrHeadConflict
	}
	if err = d.apply(ctx, tx, lib.OwnerUserID, lib.ID, now); err != nil {
		return err
	}
	return tx.Commit()
}
