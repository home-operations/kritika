package store

import (
	"testing"
	"time"

	"github.com/home-operations/kritika/internal/tuning"
)

func TestPoolConfig(t *testing.T) {
	tests := []struct {
		name         string
		url          string
		statementMax time.Duration
		wantMax      int32
		wantTimeout  string
		wantPing     time.Duration
	}{
		{name: "the ceiling given applies", url: "postgres://u:p@db/kritika", wantMax: 16, wantPing: tuning.Get().Store.PingTimeout},
		{name: "the URI's pool_max_conns wins", url: "postgres://u:p@db/kritika?pool_max_conns=40", wantMax: 40, wantPing: tuning.Get().Store.PingTimeout},
		{name: "a keyword DSN's pool_max_conns wins too", url: "host=db user=u password=p dbname=kritika pool_max_conns=3", wantMax: 3, wantPing: tuning.Get().Store.PingTimeout},
		{name: "a statement timeout is set in milliseconds", url: "postgres://u:p@db/kritika", statementMax: 10 * time.Minute, wantMax: 16, wantTimeout: "600000", wantPing: tuning.Get().Store.PingTimeout},
		{name: "a statement timeout under a millisecond is one, not none", url: "postgres://u:p@db/kritika", statementMax: 500 * time.Microsecond, wantMax: 16, wantTimeout: "1", wantPing: tuning.Get().Store.PingTimeout},
		{name: "a statement timeout of part of a millisecond rounds up", url: "postgres://u:p@db/kritika", statementMax: 1500 * time.Microsecond, wantMax: 16, wantTimeout: "2", wantPing: tuning.Get().Store.PingTimeout},
		{name: "the URI's pool_ping_timeout wins", url: "postgres://u:p@db/kritika?pool_ping_timeout=1s", wantMax: 16, wantPing: time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := poolConfig(tt.url, "kritika-test", tt.statementMax, 16)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.MaxConns != tt.wantMax {
				t.Errorf("MaxConns = %d, want %d", cfg.MaxConns, tt.wantMax)
			}
			if cfg.PingTimeout != tt.wantPing {
				t.Errorf("PingTimeout = %s, want %s", cfg.PingTimeout, tt.wantPing)
			}
			params := cfg.ConnConfig.RuntimeParams
			if params["application_name"] != "kritika-test" || params["tcp_keepalives_idle"] != "30" {
				t.Errorf("runtime params = %v", params)
			}
			if params["statement_timeout"] != tt.wantTimeout {
				t.Errorf("statement_timeout = %q, want %q", params["statement_timeout"], tt.wantTimeout)
			}
		})
	}
}

// TestPoolConfigKeepalives: the keepalive timings are tuning's, in seconds.
func TestPoolConfigKeepalives(t *testing.T) {
	tuning.Override(t, func(v *tuning.Values) {
		v.Store.KeepaliveIdle, v.Store.KeepaliveInterval, v.Store.KeepaliveCount = 45*time.Second, 2500*time.Millisecond, 5
	})
	cfg, err := poolConfig("postgres://u:p@db/kritika", "kritika-test", 0, 16)
	if err != nil {
		t.Fatal(err)
	}
	p := cfg.ConnConfig.RuntimeParams
	if p["tcp_keepalives_idle"] != "45" || p["tcp_keepalives_interval"] != "2" || p["tcp_keepalives_count"] != "5" {
		t.Fatalf("runtime params = %v", p)
	}
}
