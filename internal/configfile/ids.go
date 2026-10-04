package configfile

import (
	"crypto/sha1"
	"strings"
	"uuid"
)

// newSHA1 is the version 5 UUID of name in space, as RFC 9562 defines it.
func newSHA1(space uuid.UUID, name string) uuid.UUID {
	h := sha1.New()
	h.Write(space[:])
	h.Write([]byte(name))
	var u uuid.UUID
	copy(u[:], h.Sum(nil))
	u[6] = u[6]&0x0f | 0x50
	u[8] = u[8]&0x3f | 0x80
	return u
}

// namespace roots every deterministic identifier kritika derives. Accounts
// and connections get their ids from their names so that any role can
// address them without a lookup that row-level security would forbid before
// the account is known: the webhook listener derives the account id from the
// repository owner a webhook names and opens the account transaction
// directly. It is itself derived from the URL namespace of RFC 9562.
var namespace = newSHA1(uuid.MustParse("6ba7b811-9dad-11d1-80b4-00c04fd430c8"), "https://github.com/home-operations/kritika")

// ID is the account's stable identifier, derived from its forge and name.
func (a *Account) ID() string { return AccountID(a.Forge, a.Name) }

// Key is how the account is named across kritika: "<forge>/<name>",
// lowercased, since forge account names are not case sensitive.
func (a *Account) Key() string { return AccountKey(a.Forge, a.Name) }

// AccountKey is the key of the account name on forge.
func AccountKey(forge Forge, name string) string {
	return strings.ToLower(string(forge) + "/" + name)
}

// AccountID is the id of the account name on forge.
func AccountID(forge Forge, name string) string {
	return newSHA1(namespace, "account:"+AccountKey(forge, name)).String()
}

// ID is the connection's stable identifier, derived from its name.
func (i *Connection) ID() string {
	return newSHA1(namespace, "connection:"+i.Name).String()
}

// RepositoryID is the stable identifier of a repository of an account,
// derived from the account's id and the repository's full name, lowercased
// since GitHub names are not case sensitive, so ingest can upsert it
// without first reading it back, however a name is spelled.
func RepositoryID(accountID, fullName string) string {
	return newSHA1(namespace, "repository:"+accountID+":"+strings.ToLower(fullName)).String()
}

// Slug is how the dashboard names the account in its URLs and lists:
// "<forge>/<name>", with the name as the configuration spells it.
func (a *Account) Slug() string { return string(a.Forge) + "/" + a.Name }
