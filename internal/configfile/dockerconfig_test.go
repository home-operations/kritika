package configfile

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// writeDockerConfig replaces the file at path with content the way the
// kubelet updates a mounted Secret: a new file renamed over the old.
func writeDockerConfig(t *testing.T, path, content string) {
	t.Helper()
	next := path + ".next"
	if err := os.WriteFile(next, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(next, path); err != nil {
		t.Fatal(err)
	}
}

func dockerAuth(username, password string) string {
	return base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
}

// TestEgressDockerConfig: a credential read from a Docker config takes its
// host's entry, under a bare host or a legacy URL key. It follows the file
// when the file changes, and keeps the credential read last while the
// changed file does not read.
func TestEgressDockerConfig(t *testing.T) {
	minimalEnv(t)
	path := filepath.Join(t.TempDir(), ".dockerconfigjson")
	writeDockerConfig(t, path, `{"auths": {
		"git.example.com": {"auth": "`+dockerAuth("bot", "pw-1")+`"},
		"https://registry.example.com/v1/": {"username": "robot", "password": "pw-r"}}}`)
	f, err := loadBytes(t, []byte(`egress:
  allow: [git.example.com, registry.example.com]
  credentials:
    git.example.com: { scheme: oci, dockerConfigJson: `+path+`, accounts: [acme] }
    registry.example.com: { scheme: basic, dockerConfigJson: `+path+`, accounts: [acme] }
`+minimal))
	if err != nil {
		t.Fatal(err)
	}
	check := func(host, username, secret string) {
		t.Helper()
		if c := f.EgressRules().Credentials[host]; c.Username != username || c.Secret != secret {
			t.Fatalf("%s = %q, %q; want %q, %q", host, c.Username, c.Secret, username, secret)
		}
	}
	check("git.example.com", "bot", "pw-1")
	check("registry.example.com", "robot", "pw-r")

	writeDockerConfig(t, path, `{"auths": {"git.example.com": {"auth": "`+dockerAuth("bot", "pw-2")+`"}}}`)
	check("git.example.com", "bot", "pw-2")
	// The rotation revokes nothing, so the mask keeps the value it replaced.
	secrets := f.EgressSecrets()
	for _, want := range []string{"pw-1", dockerAuth("bot", "pw-1"), "pw-2", dockerAuth("bot", "pw-2"), "pw-r", dockerAuth("robot", "pw-r")} {
		if !slices.Contains(secrets, want) {
			t.Errorf("EgressSecrets() lacks %q", want)
		}
	}
	// registry.example.com's entry is gone from the changed file.
	check("registry.example.com", "robot", "pw-r")

	writeDockerConfig(t, path, `{"auths": `)
	check("git.example.com", "bot", "pw-2")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	check("git.example.com", "bot", "pw-2")
}

func TestEgressDockerConfigRejects(t *testing.T) {
	minimalEnv(t)
	t.Setenv("TEST_GH_TOKEN", "ghp_x")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	writeDockerConfig(t, path, `{"auths": {
		"git.example.com": {"auth": "`+dockerAuth("bot", "pw")+`"},
		"token.example.com": {"identitytoken": "refresh"},
		"bad.example.com": {"auth": "`+base64.StdEncoding.EncodeToString([]byte("no-colon-secret"))+`"}}}`)
	entry := func(host, fields string) string {
		return "egress:\n  allow: [\"*\"]\n  credentials:\n    " + host + ": { " + fields + ", accounts: [acme] }\n"
	}
	for name, tt := range map[string]struct{ yaml, want string }{
		"a missing file":     {entry("git.example.com", "scheme: oci, dockerConfigJson: "+filepath.Join(dir, "absent.json")), "no such file"},
		"no entry":           {entry("other.example.com", "scheme: oci, dockerConfigJson: "+path), "auths has no username and password for other.example.com"},
		"an identity token":  {entry("token.example.com", "scheme: oci, dockerConfigJson: "+path), "holds a token"},
		"an auth that fails": {entry("bad.example.com", "scheme: oci, dockerConfigJson: "+path), "is not a username and password"},
		"the bearer scheme":  {entry("git.example.com", "dockerConfigJson: "+path), "for the basic or oci scheme"},
		"a token beside it":  {entry("git.example.com", "scheme: oci, token: { env: TEST_GH_TOKEN }, dockerConfigJson: "+path), "takes no token or username"},
		"a username beside":  {entry("git.example.com", "scheme: oci, username: bot, dockerConfigJson: "+path), "takes no token or username"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := loadBytes(t, []byte(tt.yaml+minimal))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
			if strings.Contains(err.Error(), "no-colon-secret") {
				t.Fatalf("the error quotes the entry's secret: %v", err)
			}
		})
	}
}
