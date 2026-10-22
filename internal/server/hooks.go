package server

import (
	"net/http"
	"strings"
)

// Public is the handler of the one listener a forge and a browser reach.
// POST /hooks/{connection}, and the same under basePath, the dashboard
// URL's path, goes to hooks, which checks each delivery's signature itself;
// GET /forgejo/.well-known/{document}, and the same under basePath, goes
// to issuer, the documents a Forgejo instance reads kritika's signing keys
// from; everything else goes to web, the dashboard, which signs its own
// requests in.
func Public(basePath string, hooks, issuer, web http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("POST /hooks/{connection}", hooks)
	mux.Handle("GET /forgejo/.well-known/{document}", issuer)
	if base := strings.TrimRight(basePath, "/"); base != "" {
		mux.Handle("POST "+base+"/hooks/{connection}", hooks)
		mux.Handle("GET "+base+"/forgejo/.well-known/{document}", issuer)
	}
	mux.Handle("/", web)
	return mux
}
