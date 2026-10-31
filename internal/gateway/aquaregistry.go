package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/home-operations/kritika/internal/aqua"
)

const (
	// maxAquaRegistryBytes bounds the registry's fetch, many times its
	// size.
	maxAquaRegistryBytes = 64 << 20
	// aquaRegistryTimeout bounds one fetch of it.
	aquaRegistryTimeout = 2 * time.Minute
)

// aquaRegistryRetry is how soon a replica that has no registry yet asks
// again. So a replica that started while its source was down serves tools
// soon after the source is back. It is a variable for the tests.
var aquaRegistryRetry = time.Minute

// AquaRegistry is the aqua registry that the gateway keeps for runners to
// resolve repository tools against. It is the file at URL, which the gateway
// asks for again every Refresh with the validators of the last answer. The
// gateway takes the file only once it parses as a registry. So a fetch that
// fails, or that answers with something else, leaves the last registry
// served. Runners read it from the gateway, not from its source. So a runner
// pod needs no egress for it, and the source sees a replica's fetch each
// Refresh, not one fetch per run.
type AquaRegistry struct {
	URL     string
	Refresh time.Duration
	Client  *http.Client
	Logger  *slog.Logger

	mu             sync.RWMutex
	body           []byte
	etag, modified string
}

// Run keeps the registry until ctx ends.
func (a *AquaRegistry) Run(ctx context.Context) error {
	for {
		if err := a.refresh(ctx); err != nil && ctx.Err() == nil {
			a.Logger.Warn("aqua registry not refreshed", "url", a.URL, "error", err)
		}
		wait := a.Refresh
		if a.current() == nil {
			wait = min(wait, aquaRegistryRetry)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
	}
}

func (a *AquaRegistry) refresh(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, aquaRegistryTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return err
	}
	a.mu.RLock()
	if a.body != nil {
		if a.etag != "" {
			req.Header.Set("If-None-Match", a.etag)
		}
		if a.modified != "" {
			req.Header.Set("If-Modified-Since", a.modified)
		}
	}
	a.mu.RUnlock()
	resp, err := a.Client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusNotModified:
		return nil
	case http.StatusOK:
	default:
		return errors.New(resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAquaRegistryBytes+1))
	switch {
	case err != nil:
		return err
	case len(body) > maxAquaRegistryBytes:
		return fmt.Errorf("larger than %d bytes", maxAquaRegistryBytes)
	}
	registry, err := aqua.Parse(body)
	if err != nil {
		return err
	}
	packages := registry.Len()
	if packages == 0 {
		return errors.New("it has no packages")
	}
	a.mu.Lock()
	a.body, a.etag, a.modified = body, resp.Header.Get("ETag"), resp.Header.Get("Last-Modified")
	a.mu.Unlock()
	a.Logger.Info("aqua registry refreshed", "url", a.URL, "bytes", len(body), "packages", packages)
	return nil
}

// current is the registry kept, nil before a fetch is taken.
func (a *AquaRegistry) current() []byte {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.body
}

// aquaRegistry serves the registry kept to a runner with a live run token.
func (g *Server) aquaRegistry(w http.ResponseWriter, r *http.Request) {
	if _, ok := g.admit(w, r); !ok {
		return
	}
	var body []byte
	if g.AquaRegistry != nil {
		body = g.AquaRegistry.current()
	}
	if body == nil {
		refuse(w, http.StatusServiceUnavailable, "unavailable", "the gateway has not fetched the aqua registry yet")
		return
	}
	w.Header().Set("Content-Type", "application/yaml")
	_, _ = w.Write(body)
}
