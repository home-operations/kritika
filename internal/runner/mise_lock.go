package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/home-operations/kritika/internal/textcut"
)

// lock has mise lock t for this platform, into the run's own lock beside
// the repository's config, and reports the URLs the lock then pins
// downloads on. mise resolves the download through the tool's backend
// and takes its checksum from what the backend publishes, the GitHub
// API's asset digest or a checksum file of the release, else by fetching
// the asset itself; nothing of that goes through the gateway's cache, so
// an entry the cache serves the install is checked against a checksum
// the cache never chose. A tool the lock does not pin with a URL and a
// checksum for this platform is an error.
func (m *miseTools) lock(ctx context.Context, t *miseTool) ([]string, error) {
	lctx, cancel := context.WithTimeout(ctx, installTimeout)
	defer cancel()
	saved := m.env
	m.env = append(slices.Clone(m.env), "MISE_GITHUB_TOKEN="+m.secrets.GitToken)
	defer func() { m.env = saved }()
	out := &cappedWriter{max: installOutputBytes}
	var lock []byte
	err := m.unlocked(ctx, func() error {
		if err := m.exec(lctx, out, out, "lock", "--platform", misePlatform(), t.name); err != nil {
			return fmt.Errorf("mise lock: %w: %s", err, m.secrets.Mask(textcut.Prefix(strings.TrimSpace(string(out.buf)), installOutputBytes)))
		}
		var err error
		lock, err = os.ReadFile(filepath.Join(m.home, "mise.lock"))
		return err
	})
	switch {
	case err != nil && lctx.Err() != nil && ctx.Err() == nil:
		return nil, fmt.Errorf("mise lock: stopped after %s", installTimeout)
	case err != nil:
		return nil, err
	}
	locked, urls, err := lockedTools(string(lock), misePlatform())
	if err != nil {
		return nil, err
	}
	if !slices.Contains(locked[t.name], t.version) {
		return nil, fmt.Errorf("the lock does not pin %s@%s with a URL and checksum for %s", t.name, t.version, misePlatform())
	}
	return urls, nil
}
