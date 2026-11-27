// Package tuning holds kritika's behavioural constants: the caps, budgets,
// timeouts, intervals, batch sizes and retry counts that the other packages
// work within. The tunable ones are fields of Values, which an operator
// overrides with a KRITIKA_TUNING_* environment variable or the config
// file's tuning block (never both for one field). The fixed ones, which an
// external protocol or the database schema decides, are constants.
//
// The package imports nothing of kritika's, so every package, the runner's
// included, can import it.
//
// # Adding a tunable field
//
// A field goes in its area's struct, one per owning package (Gateway, Store,
// Model, ...), declared in the file of its subsystem (gateway.go, store.go,
// model.go, ...), and carries:
//
//   - env:"KRITIKA_TUNING_<AREA>_<NAME>", the full name, spelled out: AREA is
//     the area's YAML key and NAME the field's YAML key, both in UPPER_SNAKE
//     (gateway.similarPerQuery is KRITIKA_TUNING_GATEWAY_SIMILAR_PER_QUERY,
//     contextPack.similarLimit is KRITIKA_TUNING_CONTEXT_PACK_SIMILAR_LIMIT),
//     except that gitHub, gitLab and chatGpt are GITHUB, GITLAB and CHATGPT;
//   - envDefault:"<value>", the default, the only place it is written;
//   - yaml:"<lowerCamelName>,omitempty", so the file path is
//     tuning.<area>.<name>;
//   - tuning:"zero" only when 0 (or an empty list) is a meaningful value:
//     Validate otherwise refuses anything but a positive one;
//   - a doc comment saying what the field bounds and why the default is what
//     it is.
//
// The field's type is one of time.Duration, int, int64, float64, ByteSize or
// []time.Duration (comma-separated in the environment). A value derived from
// others is a method on the area, never a field of its own, so it cannot
// disagree with what it is derived from. An invariant between fields is a
// rule that the area's file adds to validate.go's crossField.
//
// Read a value at the point of use, tuning.Get().Gateway.SimilarMax, not
// into a package-level variable at init, which would miss the configured
// value. A test changes a value with Override. Defaults, Env and Validate
// find a new field by its tags, and the tests in this package fail until
// defaults_test.go pins its default.
package tuning

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Values is every tunable value, one field per area. Its YAML form is the
// config file's tuning block.
type Values struct {
	Gateway     Gateway     `yaml:"gateway,omitempty"`
	Egress      Egress      `yaml:"egress,omitempty"`
	Aqua        Aqua        `yaml:"aqua,omitempty"`
	ContextPack ContextPack `yaml:"contextPack,omitempty"`
	Indexer     Indexer     `yaml:"indexer,omitempty"`
	Chunk       Chunk       `yaml:"chunk,omitempty"`
	Lexical     Lexical     `yaml:"lexical,omitempty"`
	RunSpec     RunSpec     `yaml:"runSpec,omitempty"`
	Executor    Executor    `yaml:"executor,omitempty"`
	Runner      Runner      `yaml:"runner,omitempty"`

	Worker     Worker     `yaml:"worker,omitempty"`
	Store      Store      `yaml:"store,omitempty"`
	Jobs       Jobs       `yaml:"jobs,omitempty"`
	JobTimeout JobTimeout `yaml:"jobTimeout,omitempty"`
	Poller     Poller     `yaml:"poller,omitempty"`
	Ingest     Ingest     `yaml:"ingest,omitempty"`
	Review     Review     `yaml:"review,omitempty"`
	Transcript Transcript `yaml:"transcript,omitempty"`
	PRFilter   PRFilter   `yaml:"prFilter,omitempty"`

	Model      Model      `yaml:"model,omitempty"`
	Agent      Agent      `yaml:"agent,omitempty"`
	ChatGPT    ChatGPT    `yaml:"chatGpt,omitempty"`
	Forge      Forge      `yaml:"forge,omitempty"`
	GitHub     GitHub     `yaml:"gitHub,omitempty"`
	GitLab     GitLab     `yaml:"gitLab,omitempty"`
	Forgejo    Forgejo    `yaml:"forgejo,omitempty"`
	Webhook    Webhook    `yaml:"webhook,omitempty"`
	WebAPI     WebAPI     `yaml:"webApi,omitempty"`
	Server     Server     `yaml:"server,omitempty"`
	Auth       Auth       `yaml:"auth,omitempty"`
	Linear     Linear     `yaml:"linear,omitempty"`
	Upstream   Upstream   `yaml:"upstream,omitempty"`
	Adapter    Adapter    `yaml:"adapter,omitempty"`
	Metrics    Metrics    `yaml:"metrics,omitempty"`
	RoleMap    RoleMap    `yaml:"roleMap,omitempty"`
	RepoConfig RepoConfig `yaml:"repoConfig,omitempty"`
	ConfigFile ConfigFile `yaml:"configFile,omitempty"`
	Process    Process    `yaml:"process,omitempty"`
}

// Fixed values: the limits, addresses and names that an external service, a
// protocol or the database schema sets, which no operator can change, here
// for every package that speaks to it. Each says where it comes from. A
// constant only one package needs, a heuristic of its own above all, stays
// in that package.
const (
	// SecretBytes is the most that Kubernetes stores in one Secret, which a
	// run's spec travels in beside its credentials.
	SecretBytes = 1 << 20
	// S3MaxPutBytes is the largest object that one S3 PutObject uploads.
	S3MaxPutBytes = 5 << 30

	// GitHubAPIHost is the host of GitHub's REST and GraphQL APIs, and
	// GitHubAPIURL their base.
	GitHubAPIHost = "api.github.com"
	GitHubAPIURL  = "https://" + GitHubAPIHost
	// GitHubAccept is the media type of GitHub's REST answers, and
	// GitHubAPIVersion the REST API version that kritika speaks, sent in
	// the GitHubAPIVersionHeader.
	GitHubAccept           = "application/vnd.github+json"
	GitHubAPIVersionHeader = "X-GitHub-Api-Version"
	GitHubAPIVersion       = "2022-11-28"
	// GitHubCommentBytes is the longest comment body GitHub takes, in
	// characters, and so in bytes at least.
	GitHubCommentBytes = 65536
	// GitHubMaxJWTLifetime is the longest an App JWT can live: GitHub
	// refuses one that expires further ahead.
	GitHubMaxJWTLifetime = 10 * time.Minute

	// OpenAIBaseURL is OpenAI's API, where an openai or chatgpt provider
	// without a baseUrl goes, and the resource a ChatGPT plan's tokens are
	// for.
	OpenAIBaseURL = "https://api.openai.com/v1"

	// BearerPrefix starts an Authorization header that carries a bearer
	// token.
	BearerPrefix = "Bearer "
	// HTTPSPort is the only port that egress and a runner's repository
	// tools reach a destination on.
	HTTPSPort = "443"

	// Product is how kritika names itself to the services it calls: its
	// User-Agent when it has no version to give.
	Product = "kritika"
)

// UserAgent is the User-Agent of kritika at version.
func UserAgent(version string) string { return Product + "/" + version }

// ByteSize is a size in bytes, a whole number of them or one with a
// binary suffix as Kubernetes writes a quantity: Ki, Mi, Gi or Ti.
type ByteSize int64

// byteSuffixes are ByteSize's units, largest first.
var byteSuffixes = []struct {
	suffix string
	unit   int64
}{{"Ti", 1 << 40}, {"Gi", 1 << 30}, {"Mi", 1 << 20}, {"Ki", 1 << 10}}

// UnmarshalText parses the variable's value.
func (b *ByteSize) UnmarshalText(text []byte) error {
	s := string(text)
	num, unit := s, int64(1)
	for _, u := range byteSuffixes {
		if n, ok := strings.CutSuffix(s, u.suffix); ok {
			num, unit = n, u.unit
			break
		}
	}
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil || n < 0 || n > math.MaxInt64/unit {
		return fmt.Errorf("tuning: %q is not a size: a whole number of bytes, or one followed by Ki, Mi, Gi or Ti", s)
	}
	*b = ByteSize(n * unit)
	return nil
}

// MarshalText writes b with the largest suffix that divides it exactly, or
// as plain bytes, so UnmarshalText reads back the same size.
func (b ByteSize) MarshalText() ([]byte, error) { return []byte(b.String()), nil }

func (b ByteSize) String() string {
	n := int64(b)
	if n > 0 {
		for _, u := range byteSuffixes {
			if n%u.unit == 0 {
				return strconv.FormatInt(n/u.unit, 10) + u.suffix
			}
		}
	}
	return strconv.FormatInt(n, 10)
}
