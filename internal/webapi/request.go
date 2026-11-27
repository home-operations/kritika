package webapi

import (
	"errors"
	"net/http"

	jsonv2 "encoding/json/v2"

	"github.com/home-operations/kritika/internal/tuning"
)

// metaPath is the one API route served without a session.
const metaPath = "/api/v1/meta"

var errForbidden = errStatus(http.StatusForbidden, CodeForbidden, "this needs an admin")

func (s *Server) getMeta(w http.ResponseWriter, _ *http.Request) error {
	writeJSON(w, http.StatusOK, Meta{Version: s.version, WebURL: s.webURL.String()})
	return nil
}

// readBody strictly decodes one JSON document into v: a member v does
// not have, a name given twice or anything after the document is refused.
func readBody(r *http.Request, v any) error {
	body := http.MaxBytesReader(nil, r.Body, int64(tuning.Get().WebAPI.MaxBodyBytes))
	err := jsonv2.UnmarshalRead(body, v, jsonv2.RejectUnknownMembers(true))
	if _, tooLarge := errors.AsType[*http.MaxBytesError](err); tooLarge {
		return errBadRequest(CodeBadRequest, "request body is too large")
	}
	if err != nil {
		return errBadRequest(CodeBadRequest, "request body is not valid JSON: "+err.Error())
	}
	return nil
}
