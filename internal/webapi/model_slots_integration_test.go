//go:build integration

package webapi

import (
	"bytes"
	"context"
	"testing"

	"github.com/home-operations/kritika/internal/configfile"
	"github.com/home-operations/kritika/internal/configfile/configfiletest"
)

// testModelSlots: the queue shows the slots of a model of the instance's
// provider once, for every account, with the holds of every account
// counted, and only to a viewer of an account that holds one. An account's
// own slots show under the account. The reranker's key resolves to its
// provider's limit.
func testModelSlots(t *testing.T, e *apiEnv) {
	ctx := t.Context()
	file := configfiletest.Load(t, integrationConfig+`
providers:
  pool: { type: openai, baseUrl: http://pool.test/v1, apiKey: { env: KRITIKA_TEST_TOKEN }, concurrency: 3 }
embedding: { model: pool/embed, dims: 8, rerank: { model: pool/rerank, api: tei } }
accounts:
  wa:
    providers:
      wa-key: { type: openai, apiKey: { env: KRITIKA_TEST_TOKEN } }
`)
	e.srv.current.Set(file)
	t.Cleanup(func() { e.srv.current.Set(e.file) })
	wa, _ := file.Account(configfile.ForgeGitHub, "wa")
	wb, _ := file.Account(configfile.ForgeGitHub, "wb")
	for i, hold := range []struct {
		account *configfile.Account
		key     string
		ref     configfile.ModelRef
	}{
		{wa, "pool/big", "pool/big"},
		{wb, "pool/big", "pool/big"},
		{wb, "pool/only-b", "pool/only-b"},
		{wa, "wa-key/small", "wa-key/small"},
		{wa, "rerank:" + file.Embedding.Rerank.Model, file.Embedding.Rerank.Ref},
	} {
		lease, err := e.st.TakeLease(ctx, hold.account.ID(), hold.key, file.Slots(hold.account, hold.ref), int64(-9000-i))
		if err != nil || lease == nil {
			t.Fatalf("TakeLease of %s = %v, %v", hold.key, lease, err)
		}
		t.Cleanup(func() { _, _ = lease.Release(context.Background()) })
	}
	const shared = `{"account":"","model":"pool/big","held":2,"slots":3}`
	for _, tt := range []struct {
		who  string
		want []string
		not  []string
	}{
		{"admin", []string{
			shared, `{"account":"","model":"pool/only-b","held":1,"slots":3}`,
			`{"account":"github/wa","model":"wa-key/small","held":1,"slots":2}`,
			`{"account":"","model":"rerank:rerank","held":1,"slots":3}`,
		}, nil},
		{"member-a", []string{shared}, []string{"pool/only-b"}},
		{"member-b", []string{shared, "pool/only-b"}, []string{"wa-key/small", "rerank:rerank"}},
	} {
		t.Run(tt.who, func(t *testing.T) {
			status, body := e.getBody(tt.who, "/api/v1/queue")
			if status != 200 {
				t.Fatalf("status = %d: %s", status, body)
			}
			for _, w := range tt.want {
				if !bytes.Contains(body, []byte(w)) {
					t.Errorf("body lacks %s: %s", w, body)
				}
			}
			for _, n := range tt.not {
				if bytes.Contains(body, []byte(n)) {
					t.Errorf("body contains %s: %s", n, body)
				}
			}
			if n := bytes.Count(body, []byte(`"model":"pool/big"`)); n != 1 {
				t.Errorf("the shared slots show %d times, want once: %s", n, body)
			}
		})
	}
}
