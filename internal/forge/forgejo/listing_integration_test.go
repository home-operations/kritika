//go:build integration

package forgejo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestRepositoriesIntegration lists, against real Gitea and Forgejo
// instances, every kind of account a connection serves and checks the
// bot is given every repository it can reach there, and only those.
// KRITIKA_TEST_FORGES names the instances, as name=URL pairs separated
// by commas, each with a site admin KRITIKA_TEST_FORGE_ADMIN (user:password)
// signs in as to build the fixtures; mise run test-forges starts them.
func TestRepositoriesIntegration(t *testing.T) {
	forges := os.Getenv("KRITIKA_TEST_FORGES")
	if forges == "" {
		t.Skip("KRITIKA_TEST_FORGES is required for forge listing integration tests")
	}
	user, password, ok := strings.Cut(os.Getenv("KRITIKA_TEST_FORGE_ADMIN"), ":")
	if !ok {
		t.Fatal("KRITIKA_TEST_FORGE_ADMIN must be user:password")
	}
	for pair := range strings.SplitSeq(forges, ",") {
		name, base, ok := strings.Cut(pair, "=")
		if !ok {
			t.Fatalf("KRITIKA_TEST_FORGES entry %q is not name=URL", pair)
		}
		t.Run(name, func(t *testing.T) {
			testRepositories(t, &forgeAdmin{t: t, base: strings.TrimSuffix(base, "/"), user: user, password: password})
		})
	}
}

func testRepositories(t *testing.T, admin *forgeAdmin) {
	// Every run builds its accounts under a fresh suffix, so the suite
	// re-runs against the same instances.
	sfx := fmt.Sprintf("%x", time.Now().UnixNano())[8:]
	bot, dev := "bot-"+sfx, "dev-"+sfx
	admin.createUser(bot)
	admin.createUser(dev)
	// The scopes the setup docs give the connection's token.
	token := admin.token(bot, "read:user", "write:repository", "write:issue")
	want := map[string][]string{}

	// A public organization with more repositories than one page holds,
	// some private, all reached through a team with every repository.
	acme := "acme-" + sfx
	admin.createOrg(acme, "public")
	for i := range 60 {
		admin.createOrgRepo(acme, fmt.Sprintf("repo-%02d", i), i%3 == 0, false)
		want[acme] = append(want[acme], fmt.Sprintf("%s/repo-%02d", acme, i))
	}
	admin.createOrgRepo(acme, "old", false, false)
	admin.archive(acme, "old")
	want[acme] = append(want[acme], acme+"/old")
	admin.addTeam(acme, true, bot)

	// A private organization the bot reaches part of, through a team
	// with chosen repositories alone: those, and the public ones, which
	// every member reads.
	hidden := "hidden-" + sfx
	admin.createOrg(hidden, "private")
	team := admin.addTeam(hidden, false, bot)
	for i := range 5 {
		repo := fmt.Sprintf("svc-%d", i)
		private := i%2 == 0
		admin.createOrgRepo(hidden, repo, private, false)
		if i < 3 {
			admin.addTeamRepo(team, hidden, repo)
		}
		if i < 3 || !private {
			want[hidden] = append(want[hidden], hidden+"/"+repo)
		}
	}

	// An organization of private repositories the bot reaches few of, so
	// most pages hold none it sees.
	sparse := "sparse-" + sfx
	admin.createOrg(sparse, "public")
	team = admin.addTeam(sparse, false, bot)
	for i := range 120 {
		repo := fmt.Sprintf("app-%03d", i)
		admin.createOrgRepo(sparse, repo, true, false)
		if i%25 == 24 {
			admin.addTeamRepo(team, sparse, repo)
			want[sparse] = append(want[sparse], sparse+"/"+repo)
		}
	}

	// A limited organization, visible to signed-in users, with every
	// repository reached through a team.
	limited := "limited-" + sfx
	admin.createOrg(limited, "limited")
	for i := range 4 {
		repo := fmt.Sprintf("lib-%d", i)
		admin.createOrgRepo(limited, repo, i%2 == 1, false)
		want[limited] = append(want[limited], limited+"/"+repo)
	}
	admin.addTeam(limited, true, bot)

	// A user account: its public repositories, a fork, a private one the
	// bot collaborates on, and a private one it does not reach.
	admin.createOrgRepo(acme, "upstream", false, true)
	want[acme] = append(want[acme], acme+"/upstream")
	for i := range 3 {
		admin.createUserRepo(dev, fmt.Sprintf("tool-%d", i), false)
		want[dev] = append(want[dev], fmt.Sprintf("%s/tool-%d", dev, i))
	}
	admin.fork(acme, "upstream", dev)
	want[dev] = append(want[dev], dev+"/upstream")
	admin.createUserRepo(dev, "shared", true)
	admin.addCollaborator(dev, "shared", bot)
	want[dev] = append(want[dev], dev+"/shared")
	admin.createUserRepo(dev, "secret", true)

	// The bot's own account.
	admin.createUserRepo(bot, "own", true)
	want[bot] = []string{bot + "/own"}

	c, err := NewGiteaClient(admin.base, token, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, account := range slices.Sorted(maps.Keys(want)) {
		t.Run(strings.TrimSuffix(account, "-"+sfx), func(t *testing.T) {
			repos, err := c.Repositories(t.Context(), account)
			if err != nil {
				t.Fatalf("Repositories(%s): %v", account, err)
			}
			got := map[string]Repository{}
			for _, r := range repos {
				if _, dup := got[r.FullName]; dup {
					t.Errorf("%s listed twice", r.FullName)
				}
				got[r.FullName] = r
			}
			for _, name := range want[account] {
				if _, ok := got[name]; !ok {
					t.Errorf("%s is missing", name)
				}
			}
			for name := range got {
				if !slices.Contains(want[account], name) {
					t.Errorf("%s is listed, but the bot does not reach it", name)
				}
			}
			if t.Failed() {
				t.Logf("listed %d of %d", len(got), len(want[account]))
			}
			for name, r := range got {
				private := admin.repo(name).Private
				if (r.Visibility == "private") != private {
					t.Errorf("%s visibility = %q, private = %t", name, r.Visibility, private)
				}
				if r.Archived != strings.HasSuffix(name, "/old") || r.Fork != (name == dev+"/upstream") {
					t.Errorf("%s archived = %t, fork = %t", name, r.Archived, r.Fork)
				}
			}
		})
	}
}

// forgeAdmin builds fixtures through the API as a site admin.
type forgeAdmin struct {
	t              *testing.T
	base           string
	user, password string
}

// call sends body to path as user, authenticated by password, and decodes
// the answer into out, failing the test on any status but 2xx.
func (a *forgeAdmin) call(user, password, method, path string, body, out any) {
	a.t.Helper()
	var r io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			a.t.Fatal(err)
		}
		r = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(a.t.Context(), method, a.base+"/api/v1"+path, r)
	if err != nil {
		a.t.Fatal(err)
	}
	req.SetBasicAuth(user, password)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		a.t.Fatalf("%s %s: %d %s", method, path, resp.StatusCode, data)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			a.t.Fatalf("%s %s: %v", method, path, err)
		}
	}
}

func (a *forgeAdmin) do(method, path string, body, out any) {
	a.t.Helper()
	a.call(a.user, a.password, method, path, body, out)
}

const fixturePassword = "fixture-Passw0rd!"

func (a *forgeAdmin) createUser(name string) {
	a.t.Helper()
	a.do("POST", "/admin/users", map[string]any{
		"username": name, "email": name + "@example.com", "password": fixturePassword, "must_change_password": false,
	}, nil)
}

// token is an access token of user with scopes, which only the user
// itself may create.
func (a *forgeAdmin) token(user string, scopes ...string) string {
	a.t.Helper()
	var out struct {
		SHA1 string `json:"sha1"`
	}
	a.call(user, fixturePassword, "POST", "/users/"+user+"/tokens", map[string]any{"name": "kritika", "scopes": scopes}, &out)
	return out.SHA1
}

func (a *forgeAdmin) createOrg(name, visibility string) {
	a.t.Helper()
	a.do("POST", "/orgs", map[string]any{"username": name, "visibility": visibility}, nil)
}

// createOrgRepo creates a repository under org; an initialized one has a
// commit, which a fork needs.
func (a *forgeAdmin) createOrgRepo(org, name string, private, initialized bool) {
	a.t.Helper()
	a.do("POST", "/orgs/"+org+"/repos", map[string]any{"name": name, "private": private, "auto_init": initialized}, nil)
}

func (a *forgeAdmin) createUserRepo(user, name string, private bool) {
	a.t.Helper()
	a.do("POST", "/admin/users/"+user+"/repos", map[string]any{"name": name, "private": private}, nil)
}

func (a *forgeAdmin) archive(owner, repo string) {
	a.t.Helper()
	a.do("PATCH", "/repos/"+owner+"/"+repo, map[string]any{"archived": true}, nil)
}

// fork forks owner/repo into user's account, as the user.
func (a *forgeAdmin) fork(owner, repo, user string) {
	a.t.Helper()
	a.call(user, fixturePassword, "POST", "/repos/"+owner+"/"+repo+"/forks", map[string]any{}, nil)
}

// addTeam adds a team with write access to org, holding every repository
// or only those added to it, with member in it, and returns its id.
func (a *forgeAdmin) addTeam(org string, all bool, member string) int64 {
	a.t.Helper()
	var team struct {
		ID int64 `json:"id"`
	}
	a.do("POST", "/orgs/"+org+"/teams", map[string]any{
		"name": "reviewers", "permission": "write", "includes_all_repositories": all,
		"units": []string{"repo.code", "repo.issues", "repo.pulls"},
	}, &team)
	a.do("PUT", fmt.Sprintf("/teams/%d/members/%s", team.ID, member), nil, nil)
	return team.ID
}

func (a *forgeAdmin) addTeamRepo(team int64, org, repo string) {
	a.t.Helper()
	a.do("PUT", fmt.Sprintf("/teams/%d/repos/%s/%s", team, org, repo), nil, nil)
}

func (a *forgeAdmin) addCollaborator(owner, repo, user string) {
	a.t.Helper()
	a.do("PUT", "/repos/"+owner+"/"+repo+"/collaborators/"+user, map[string]any{"permission": "write"}, nil)
}

// repo is fullName as the admin sees it.
func (a *forgeAdmin) repo(fullName string) repository {
	a.t.Helper()
	var r repository
	a.do("GET", "/repos/"+fullName, nil, &r)
	return r
}
