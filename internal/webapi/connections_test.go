package webapi

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"testing"

	"github.com/home-operations/kritika/internal/configfile/configfiletest"
)

func TestAppCacheSharesAnAppPerConnection(t *testing.T) {
	for _, name := range []string{"TEST_KEY_A", "TEST_KEY_B"} {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv(name, string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})))
	}
	t.Setenv("TEST_SECRET", "whsec")
	f := configfiletest.Load(t, `apps:
  acme-bot: { accounts: [acme], clientId: Iv1.acme, privateKey: { env: TEST_KEY_A }, webhookSecret: { env: TEST_SECRET } }
  globex-bot: { accounts: [globex], clientId: Iv1.globex, privateKey: { env: TEST_KEY_B }, webhookSecret: { env: TEST_SECRET } }
`)
	acme, _ := f.Connection("acme-bot")
	globex, _ := f.Connection("globex-bot")
	var c appCache
	first, err := c.get(acme, "")
	if err != nil {
		t.Fatal(err)
	}
	again, err := c.get(acme, "")
	if err != nil || again != first {
		t.Fatalf("a second request got another App (%v)", err)
	}
	other, err := c.get(globex, "")
	if err != nil || other == first {
		t.Fatalf("another connection shares the App (%v)", err)
	}
}
