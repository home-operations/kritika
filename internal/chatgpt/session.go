package chatgpt

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

// refreshLead is how long before the access token expires the session
// renews it, so a step's request never goes out with a token about to
// lapse under it.
const refreshLead = 5 * time.Minute

// refreshTimeout bounds one refresh when the session's client sets none.
const refreshTimeout = 30 * time.Second

// Session holds one record's tokens in memory and renews the access token
// near its expiry, one refresh at a time. It is a process's own copy of
// the session: a refresh rotates the refresh token, and a second process
// holding the record refreshes with a token OpenAI has already replaced,
// which ends its sign-in. So one process may use a record.
type Session struct {
	client   *http.Client
	tokenURL string
	now      func() time.Time

	mu    sync.Mutex
	creds Credentials
	// out is ErrSignedOut once a refresh ended the session; every Token
	// after it fails the same way, without a request.
	out error
}

// NewSession holds c, refreshing it through client, or a default one when
// nil.
func NewSession(c Credentials, client *http.Client) *Session {
	if client == nil {
		client = &http.Client{Timeout: refreshTimeout}
	}
	return &Session{client: client, tokenURL: TokenURL, now: time.Now, creds: c}
}

// Token is the access token to send now, renewed when within refreshLead
// of its expiry. A refresh that fails for a reason other than the sign-in
// ending leaves the current token in use while it lasts.
func (s *Session) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.out != nil {
		return "", s.out
	}
	now := s.now()
	if now.Before(s.creds.Expiry().Add(-refreshLead)) {
		return s.creds.AccessToken, nil
	}
	c, err := Refresh(ctx, s.client, s.tokenURL, s.creds, now)
	switch {
	case errors.Is(err, ErrSignedOut):
		s.out = err
		return "", err
	case err != nil && now.Before(s.creds.Expiry()):
		return s.creds.AccessToken, nil
	case err != nil:
		return "", err
	}
	s.creds = c
	return c.AccessToken, nil
}

// Expire marks the access token spent, after the provider refused it, so
// the next Token renews it rather than send it again for the rest of its
// hour.
func (s *Session) Expire() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.creds.ExpiresIn = 0
}
