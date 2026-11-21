package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"time"

	"github.com/mingming-cn/filecloud/internal/storage"
)

var (
	ErrCredentials  = errors.New("authentication failed")
	ErrKDFBusy      = errors.New("authentication busy")
	ErrInvalidLogin = errors.New("invalid login")
)

// Session is returned once to the client that obtained it.
type Session struct {
	AccessToken string
	ExpiresAt   time.Time
	UserID      string `json:"UserId"`
}

// A cookie and a CLI token have separate lookup namespaces in the same revocation table.
const browserNamespace = "web:"

func (h *handler) issue(ctx context.Context, req createSessionRequest, remote, namespace string) (Session, error) {
	if validateCreateSession(req) != nil {
		return Session{}, ErrInvalidLogin
	}
	_, key := storage.CanonicalUsername(req.Username)
	release, ok := h.config.limiter.tryAcquire(sourceIP(remote), key)
	if !ok {
		return Session{}, ErrKDFBusy
	}
	defer release()
	user, err := h.store.FindUserByUsername(ctx, req.Username)
	if err != nil && !errors.Is(err, storage.ErrUserNotFound) {
		return Session{}, err
	}
	hash := h.dummyHash
	if err == nil {
		hash = user.PasswordHash
	}
	valid, verifyErr := VerifyPassword([]byte(req.Password), hash, h.config.Params)
	if verifyErr != nil {
		if err == nil {
			_, dummyErr := VerifyPassword([]byte(req.Password), h.dummyHash, h.config.Params)
			verifyErr = errors.Join(verifyErr, dummyErr)
		}
		return Session{}, verifyErr
	}
	if err != nil || !valid {
		return Session{}, ErrCredentials
	}
	var raw [_accessTokenBytes]byte
	if _, err := io.ReadFull(h.config.Random, raw[:]); err != nil {
		return Session{}, err
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	now := h.config.Now().UTC()
	expires := now.Add(_sessionLifetime)
	if err := h.store.CreateVerifiedSession(ctx, user, sha256.Sum256([]byte(namespace+token)), req.DeviceName, now, expires); err != nil {
		if errors.Is(err, storage.ErrSessionNotFound) {
			return Session{}, ErrCredentials
		}
		return Session{}, err
	}
	return Session{token, expires.Truncate(time.Second), user.ID}, nil
}

func (s *Sessions) LoginBrowser(ctx context.Context, name, password, remote string) (Session, error) {
	return s.core.issue(ctx, createSessionRequest{name, password, "Web"}, remote, browserNamespace)
}
func (s *Sessions) BrowserUser(ctx context.Context, token string) (string, error) {
	return activeSession(ctx, s.core.store, s.core.config.Now(), browserNamespace, token)
}

// SetAccountPassword shares the login KDF budget with online account administration.
func (s *Sessions) SetAccountPassword(ctx context.Context, name, password, remote string, create bool) error {
	display, err := ValidateUsername(name)
	if err != nil || ValidatePassword([]byte(password)) != nil {
		return ErrInvalidLogin
	}
	_, key := storage.CanonicalUsername(display)
	release, ok := s.core.config.limiter.tryAcquire(sourceIP(remote), key)
	if !ok {
		return ErrKDFBusy
	}
	defer release()
	hash, err := HashPassword([]byte(password), s.core.config.Params, s.core.config.Random)
	if err != nil {
		return err
	}
	now := s.core.config.Now().UTC()
	if !create {
		return s.core.store.ResetPassword(ctx, display, hash, now)
	}
	id, err := NewUserID(s.core.config.Random)
	if err != nil {
		return err
	}
	return s.core.store.CreateUser(ctx, storage.User{ID: id, Username: display, PasswordHash: hash}, now)
}
func (s *Sessions) LogoutBrowser(ctx context.Context, token string) error {
	_, err := s.core.store.RevokeSession(ctx, sha256.Sum256([]byte(browserNamespace+token)), s.core.config.Now())
	return err
}
