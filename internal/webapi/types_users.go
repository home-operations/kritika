package webapi

import "time"

// The shapes of the users an admin lists, which users.go serves.

// AdminUser is a user who signed in, as an admin lists them. Access is what
// their newest session that the configuration still honors lets them read,
// nil without one. Sessions counts those sessions.
type AdminUser struct {
	User
	SignIns      []UserSignIn `json:"signIns"`
	Access       *UserAccess  `json:"access"`
	Sessions     int          `json:"sessions"`
	FirstSeenAt  time.Time    `json:"firstSeenAt"`
	LastSignInAt *time.Time   `json:"lastSignInAt"`
}

// UserSignIn is one of a user's sign-ins: the provider, as the
// configuration names its type, and the login it knows them by.
type UserSignIn struct {
	Provider string `json:"provider"`
	Login    string `json:"login"`
}

// UserAccess is what a session lets its user read: everything as an admin,
// every account, or the slugs of Accounts.
type UserAccess struct {
	Admin       bool     `json:"admin"`
	AllAccounts bool     `json:"allAccounts"`
	Accounts    []string `json:"accounts"`
}
