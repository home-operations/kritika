package configfile

import "testing"

// TestIDsAreStable pins the derived ids: rows in deployed databases are
// keyed by them, so a change here orphans every account and repository.
func TestIDsAreStable(t *testing.T) {
	account := AccountID(ForgeGitHub, "Home-Operations")
	tests := []struct {
		name, got, want string
	}{
		{"namespace", namespace.String(), "6a7920b0-5819-5377-ade3-3f95e25947ca"},
		{"account", account, "5e77e010-4135-50d2-a4c2-26588457a634"},
		{"connection", (&Connection{Name: "openrouter"}).ID(), "4c7924a1-9452-516b-8957-60417fc123fb"},
		{"repository", RepositoryID(account, "home-operations/Kritika"), "a8e386fd-f2f8-5430-8494-269384101ed7"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("id = %s, want %s", tt.got, tt.want)
			}
		})
	}
}
