package runner

import (
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/home-operations/kritika/internal/repoconfig"
	"github.com/pelletier/go-toml/v2"
)

func TestRepositoryTools(t *testing.T) {
	for _, tt := range []struct {
		commands []string
		want     bool
	}{{nil, false}, {[]string{}, false}, {[]string{"*"}, true}, {[]string{"rg"}, false}, {[]string{"*", "rg"}, false}} {
		if got := RepositoryTools(tt.commands); got != tt.want {
			t.Errorf("RepositoryTools(%v) = %v", tt.commands, got)
		}
	}
}

const repoMiseDoc = `[env]
NO_COLOR = "1"
HELM_DRIVER = "configmap"
HOME = "/evil"
FROM_ENV = "{{ get_env(name='X') }}"
COUNT = 1
_.file = ".env"
GH_TOKEN = "x"

[tools]
helm = "4.3.0"
"github:home-operations/flate" = { version = "0.6.5", filter_bins = "flate", postinstall = "flate --help" }

[settings]
url_replacements = { "https://github.com/" = "http://evil/" }

[tasks.build]
run = "make"
`

const repoMiseLockDoc = `lockfile_version = 3

[[tools.helm]]
version = "4.3.0"
backend = "aqua:helm/helm"

[tools.helm."platforms.linux-arm64"]
checksum = "sha256:31c5"
url = "https://get.helm.sh/helm-v4.3.0-linux-arm64.tar.gz"

[tools.helm."platforms.linux-x64"]
checksum = "sha256:8658"
url = "https://get.helm.sh/helm-v4.3.0-linux-amd64.tar.gz"

[[tools."github:home-operations/flate"]]
version = "0.6.5"
backend = "github:home-operations/flate"

[tools."github:home-operations/flate"."platforms.linux-arm64"]
checksum = "sha256:aaaa"
url = "https://github.com/home-operations/flate/releases/download/v0.6.5/flate_linux_arm64.tar.gz"
`

func TestKeptRepoMiseConfig(t *testing.T) {
	t.Run("no config", func(t *testing.T) {
		r, notes, err := keptRepoMiseConfig(repoconfig.Files{})
		if r != nil || notes != nil || err != nil {
			t.Fatalf("= %v, %v, %v", r, notes, err)
		}
	})
	t.Run("tools and literal env are kept, the rest dropped with notes", func(t *testing.T) {
		r, notes, err := keptRepoMiseConfig(repoconfig.Files{RepoMiseConfig: repoMiseDoc, RepoMiseLock: repoMiseLockDoc})
		if err != nil {
			t.Fatal(err)
		}
		want := []string{
			".kritika/mise.toml: tool github:home-operations/flate option postinstall dropped: only the download's options are kept",
			".kritika/mise.toml: env COUNT dropped: only a plain string is kept",
			".kritika/mise.toml: env FROM_ENV dropped: templates are not rendered",
			".kritika/mise.toml: env GH_TOKEN dropped: the name is reserved",
			".kritika/mise.toml: env HOME dropped: the name is reserved",
			".kritika/mise.toml: env _ dropped: directives are not read",
			".kritika/mise.toml: [settings] dropped: only the admin sets it",
			".kritika/mise.toml: [tasks] dropped: only the admin sets it",
		}
		if !slices.Equal(notes, want) {
			t.Fatalf("notes = %q", notes)
		}
		for _, s := range []string{"NO_COLOR = '1'", "HELM_DRIVER = 'configmap'", "helm = '4.3.0'", "filter_bins = 'flate'"} {
			if !strings.Contains(r.config, s) {
				t.Fatalf("config lacks %q:\n%s", s, r.config)
			}
		}
		for _, s := range []string{"HOME", "get_env", "settings", "tasks", "COUNT", "GH_TOKEN", "_.file", "url_replacements", "postinstall"} {
			if strings.Contains(r.config, s) {
				t.Fatalf("config keeps %q:\n%s", s, r.config)
			}
		}
		if r.lock != repoMiseLockDoc {
			t.Fatal("the lock is not kept as written")
		}
	})
	t.Run("not TOML", func(t *testing.T) {
		if _, _, err := keptRepoMiseConfig(repoconfig.Files{RepoMiseConfig: "[tools"}); err == nil || !strings.Contains(err.Error(), ".kritika/mise.toml") {
			t.Fatalf("err = %v", err)
		}
	})
}

func TestLockedTools(t *testing.T) {
	for platform, want := range map[string][]string{
		"linux-x64": {"helm"}, "linux-arm64": {"github:home-operations/flate", "helm"}, "darwin-arm64": nil,
	} {
		got, err := lockedTools(repoMiseLockDoc, platform)
		if err != nil || !slices.Equal(slices.Sorted(maps.Keys(got)), want) {
			t.Errorf("lockedTools(%s) = %v, %v; want %v", platform, got, err, want)
		}
	}
	if _, err := lockedTools("nope = [", "linux-x64"); err == nil {
		t.Fatal("no error for a lock that is not TOML")
	}
}

func TestRepoMiseEnv(t *testing.T) {
	for _, tt := range []struct {
		name, value string
		keep        bool
	}{
		{"literal", `"hello"`, true},
		{"_PRIVATE", `"literal"`, true},
		{"empty", `""`, true},
		{"expression", `"{{ get_env(name='HOME') }}"`, false},
		{"block", `"{% if true %}rendered{% endif %}"`, false},
		{"comment", `"{# comment #}literal"`, false},
		{"number", "1", false},
		{"bool", "false", false},
		{"array", `["x"]`, false},
		{"table", `{ value = "x", redact = true }`, false},
		{"_.path", `"bin"`, false},
		{"http_proxy", `"http://example.com"`, false},
		{"MISE_CONFIG_FILE", `"evil.toml"`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r, notes, err := keptRepoMiseConfig(repoconfig.Files{RepoMiseConfig: "[env]\n" + tt.name + " = " + tt.value})
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(r.config, tt.name+" =") != tt.keep || (len(notes) == 0) != tt.keep {
				t.Fatalf("config = %s, notes = %v", r.config, notes)
			}
		})
	}
}

func TestRepoMiseTools(t *testing.T) {
	doc := `[tools]
helm = ["4.3.0", "4.2.0"]
"github:home-operations/flate" = { version = "0.6.5", postinstall = "flate --help", install_env = { MODE = "{{ os() }}" }, depends = ["node"], api_url = "https://evil", filter_bins = ["flate"], exe = "flate" }
"http:tool" = { version = "1.0.0", url = "https://example.com/{{ exec(command='id') }}", headers = { Authorization = "x" } }
"http:per-platform" = { version = "1.0.0", platforms = { linux-x64 = { url = "https://example.com/x64", checksum = "sha256:ab", postinstall = "x" }, linux-arm64 = { url = "https://example.com/{{ version }}" }, windows-x64 = "no" } }
templated = "{{ get_env(name='V') }}"
"github:owner/versioned" = { version = "{{ get_env(name='V') }}", exe = "x" }
number = 1
[hooks]
enter = "echo ignored"
`
	r, notes, err := keptRepoMiseConfig(repoconfig.Files{RepoMiseConfig: doc})
	if err != nil {
		t.Fatal(err)
	}
	var kept map[string]any
	if err := toml.Unmarshal([]byte(r.config), &kept); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"helm":                         []any{"4.3.0", "4.2.0"},
		"github:home-operations/flate": map[string]any{"version": "0.6.5", "filter_bins": []any{"flate"}, "exe": "flate"},
		"http:tool":                    map[string]any{"version": "1.0.0"},
		"http:per-platform": map[string]any{"version": "1.0.0", "platforms": map[string]any{
			"linux-x64":   map[string]any{"url": "https://example.com/x64", "checksum": "sha256:ab"},
			"linux-arm64": map[string]any{},
		}},
	}
	if len(kept) != 1 || !reflect.DeepEqual(kept["tools"], want) {
		t.Fatalf("config = %s", r.config)
	}
	wantNotes := []string{
		".kritika/mise.toml: tool github:home-operations/flate option api_url dropped: only the download's options are kept",
		".kritika/mise.toml: tool github:home-operations/flate option depends dropped: only the download's options are kept",
		".kritika/mise.toml: tool github:home-operations/flate option install_env dropped: only the download's options are kept",
		".kritika/mise.toml: tool github:home-operations/flate option postinstall dropped: only the download's options are kept",
		".kritika/mise.toml: tool github:owner/versioned option version dropped: templates are not rendered",
		".kritika/mise.toml: tool http:per-platform platform linux-arm64 option url dropped: templates are not rendered",
		".kritika/mise.toml: tool http:per-platform platform linux-x64 option postinstall dropped: only the download's options are kept",
		".kritika/mise.toml: tool http:per-platform platform windows-x64 dropped: it is not a table",
		".kritika/mise.toml: tool http:tool option headers dropped: only the download's options are kept",
		".kritika/mise.toml: tool http:tool option url dropped: templates are not rendered",
		".kritika/mise.toml: tool number dropped: it is not a version or a table",
		".kritika/mise.toml: tool templated dropped: templates are not rendered",
		".kritika/mise.toml: [hooks] dropped: only the admin sets it",
	}
	if !slices.Equal(notes, wantNotes) {
		t.Fatalf("notes = %q", notes)
	}
	if bins := r.filterBins("github:home-operations/flate"); !slices.Equal(bins, []string{"flate"}) {
		t.Fatalf("filterBins = %v", bins)
	}
}

func TestLockedToolVersions(t *testing.T) {
	lock := `[[tools.tool]]
version = "1.0.0"
[tools.tool."platforms.linux-x64"]
url = "https://example.com/tool"
checksum = "sha256:abc"
[[tools.tool]]
version = "2.0.0"
[tools.tool."platforms.linux-arm64"]
url = "https://example.com/tool"
checksum = "sha256:def"
[[tools.empty]]
version = "1.0.0"
[tools.empty."platforms.linux-x64"]
url = ""
checksum = ""
[[tools.invalid]]
version = "1.0.0"
[tools.invalid."platforms.linux-x64"]
url = 1
checksum = true
`
	got, err := lockedTools(lock, "linux-x64")
	if err != nil || !reflect.DeepEqual(got, map[string][]string{"tool": {"1.0.0"}}) {
		t.Fatalf("lockedTools = %v, %v", got, err)
	}
}
