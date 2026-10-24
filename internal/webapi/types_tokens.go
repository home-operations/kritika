package webapi

import "time"

// The shapes of the personal access tokens a user manages and an admin
// lists, which tokens.go serves.

// APIToken is a personal access token as its owner or an admin lists it,
// never its value. Accounts are the configured accounts it reads unless
// AllAccounts; Owner is set in an admin's list alone.
type APIToken struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Owner       *User       `json:"owner"`
	AllAccounts bool        `json:"allAccounts"`
	Accounts    []string    `json:"accounts"`
	Status      TokenStatus `json:"status"`
	CreatedAt   time.Time   `json:"createdAt"`
	ExpiresAt   *time.Time  `json:"expiresAt"`
	LastUsedAt  *time.Time  `json:"lastUsedAt"`
}

// TokenStatus is whether a token still reads: it stops at its expiry, and
// goes stale when the sign-in configuration it came from changes, as a
// session would end.
type TokenStatus string

// Token statuses.
const (
	TokenActive  TokenStatus = "active"
	TokenExpired TokenStatus = "expired"
	TokenStale   TokenStatus = "stale"
)

// CreateTokenRequest creates a token named Name that reads Accounts, the
// slugs of accounts its creator reads, or everything they read when it
// names none, for ExpiresInDays days: 7, 30, 90 or 365, 0 for never.
type CreateTokenRequest struct {
	Name          string   `json:"name"`
	Accounts      []string `json:"accounts"`
	ExpiresInDays int      `json:"expiresInDays"`
}

// CreatedToken is a token just created, with its value, which no later
// read shows again.
type CreatedToken struct {
	APIToken
	Token string `json:"token"`
}
