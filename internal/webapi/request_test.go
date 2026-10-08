package webapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestReadBody(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantOn  bool
		wantErr string
	}{
		{name: "one document", body: `{"on":true}`, wantOn: true},
		{name: "unknown field", body: `{"on":true,"x":1}`, wantErr: `unknown object member name "x"`},
		{name: "a name twice", body: `{"on":true,"on":false}`, wantErr: `duplicate object member name "on"`},
		{name: "two documents", body: `{"on":true}{"on":false}`, wantErr: "after top-level value"},
		{name: "a name in another case", body: `{"On":true}`, wantErr: `unknown object member name "On"`},
		{name: "oversized", body: strings.Repeat(" ", maxBodyBytes+1) + "{}", wantErr: "too large"},
		{name: "empty", body: "", wantErr: "not valid JSON"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/v1/repos/1/on", strings.NewReader(tt.body))
			var req TurnOnRequest
			err := readBody(r, &req)
			if tt.wantErr == "" {
				if err != nil || req.On != tt.wantOn {
					t.Fatalf("readBody = %v, req = %+v; want on=%v", err, req, tt.wantOn)
				}
				return
			}
			e, ok := errors.AsType[*apiError](err)
			if !ok || e.status != http.StatusBadRequest || e.code != CodeBadRequest || !strings.Contains(e.message, tt.wantErr) {
				t.Fatalf("readBody = %v; want a %d %s apiError mentioning %q", err, http.StatusBadRequest, CodeBadRequest, tt.wantErr)
			}
		})
	}
}
