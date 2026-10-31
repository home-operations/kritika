package auth

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

func TestAttempts(t *testing.T) {
	now := time.Now()
	a := newAttempts(func() time.Time { return now })
	wrong := func() bool { return false }
	for range maxFailedSignIns {
		if admitted, ok := a.try("1.2.3.4", wrong); !admitted || ok {
			t.Fatal("refused before the limit")
		}
	}
	if admitted, _ := a.try("1.2.3.4", wrong); admitted {
		t.Fatal("allowed past the limit")
	}
	if admitted, _ := a.try("5.6.7.8", wrong); !admitted {
		t.Fatal("another client was held to the first's limit")
	}
	now = now.Add(failedSignInWindow)
	if admitted, _ := a.try("1.2.3.4", wrong); !admitted {
		t.Fatal("still refused after the window")
	}
}

// TestAttemptsStayBounded: a flood of failures from distinct addresses, with
// no expired window among them, cannot grow the tracker past its cap. The
// oldest window gives way, and a client already tracked keeps its count.
func TestAttemptsStayBounded(t *testing.T) {
	now := time.Now()
	a := newAttempts(func() time.Time { return now })
	wrong := func() bool { return false }
	a.try("first", wrong)
	for i := range maxTrackedClients + 100 {
		now = now.Add(time.Millisecond)
		a.try(fmt.Sprintf("2001:db8::%x", i), wrong)
	}
	if len(a.failed) > maxTrackedClients {
		t.Fatalf("%d clients tracked, cap is %d", len(a.failed), maxTrackedClients)
	}
	// "first" and the 100 oldest flood addresses gave way. The next survives.
	if _, ok := a.failed["first"]; ok {
		t.Fatal("the oldest window was not the one dropped")
	}
	if _, ok := a.failed["2001:db8::64"]; !ok {
		t.Fatal("a window newer than the oldest was dropped")
	}
	last := fmt.Sprintf("2001:db8::%x", maxTrackedClients+99)
	a.try(last, wrong)
	if got := a.failed[last].n; got != 2 || len(a.failed) > maxTrackedClients {
		t.Fatalf("count = %d with %d tracked; a tracked client failing again keeps its count and evicts nothing", got, len(a.failed))
	}
}

// TestAttemptsRightAnswersAreFree: a right password never counts, however
// many come at once. A client whose answers were all right is not tracked.
// Wrong answers after it still fill the window.
func TestAttemptsRightAnswersAreFree(t *testing.T) {
	a := newAttempts(time.Now)
	for range maxFailedSignIns * 2 {
		if admitted, ok := a.try("client", func() bool { return true }); !admitted || !ok {
			t.Fatal("a right answer consumed the limit")
		}
	}
	if len(a.failed) != 0 {
		t.Fatal("right answers left a client window")
	}
	for range maxFailedSignIns {
		a.try("client", func() bool { return false })
	}
	if admitted, _ := a.try("client", func() bool { return true }); admitted {
		t.Fatal("wrong answers after right ones did not fill the window")
	}
}

// TestAttemptsJudgeOneAtATime: attempts in flight together are judged
// under the lock, so the limit holds however many arrive at once.
func TestAttemptsJudgeOneAtATime(t *testing.T) {
	a := newAttempts(time.Now)
	const requests = maxFailedSignIns * 10
	var wg sync.WaitGroup
	results := make(chan bool, requests)
	for range requests {
		wg.Go(func() {
			admitted, _ := a.try("client", func() bool { return false })
			results <- admitted
		})
	}
	wg.Wait()
	close(results)
	admitted := 0
	for r := range results {
		if r {
			admitted++
		}
	}
	if admitted != maxFailedSignIns {
		t.Fatalf("%d of %d concurrent wrong answers were checked, want %d", admitted, requests, maxFailedSignIns)
	}
}

// TestLocalSignInRefuses covers every way that the password form says no
// without touching the store.
func TestLocalSignInRefuses(t *testing.T) {
	h := testHandler(t, "https://kritika.example.com", testFile(t, "auth:\n"+adminPassword))
	mux := http.NewServeMux()
	h.Register(mux)
	post := func(body, from string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/auth/local", strings.NewReader(body))
		r.Header.Set("X-Kritika", "1")
		r.Header.Set("Origin", "https://kritika.example.com")
		r.RemoteAddr = from + ":1234"
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	if w := post(`{"user":"admin","password":"wrong"}`, "10.0.0.1"); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", w.Code)
	} else {
		assertCode(t, w, "invalid_credentials")
	}
	if w := post(`{"user":"root","password":"s3cret"}`, "10.0.0.1"); w.Code != http.StatusUnauthorized {
		t.Fatalf("wrong user: %d", w.Code)
	}
	if w := post(`not json`, "10.0.0.1"); w.Code != http.StatusBadRequest {
		t.Fatalf("bad body: %d", w.Code)
	}
	for range maxFailedSignIns - 2 {
		post(`{"user":"admin","password":"wrong"}`, "10.0.0.1")
	}
	if w := post(`{"user":"admin","password":"s3cret"}`, "10.0.0.1"); w.Code != http.StatusTooManyRequests {
		t.Fatalf("past the limit: %d", w.Code)
	} else {
		assertCode(t, w, "too_many_attempts")
	}

	r := httptest.NewRequest(http.MethodPost, "/auth/local", strings.NewReader(`{}`))
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-origin: %d, want 403", w.Code)
	}

	h.current.Set(testFile(t, "auth:\n  github:\n    clientId: c\n    clientSecret: { env: TEST_AUTH_SECRET }\n    roleMappingExpr: '\"admin\"'\n"))
	if w := post(`{"user":"admin","password":"s3cret"}`, "10.0.0.2"); w.Code != http.StatusNotFound {
		t.Fatalf("without a password: %d, want 404", w.Code)
	}
}

type blockedPasswordBody struct {
	release <-chan struct{}
	body    *strings.Reader
}

func (b *blockedPasswordBody) Read(p []byte) (int, error) {
	<-b.release
	return b.body.Read(p)
}

func (*blockedPasswordBody) Close() error { return nil }

func TestLocalSignInConcurrentAttempts(t *testing.T) {
	h := testHandler(t, "https://kritika.example.com", testFile(t, "auth:\n"+adminPassword))
	mux := http.NewServeMux()
	h.Register(mux)
	synctest.Test(t, func(t *testing.T) {
		const requests = maxFailedSignIns * 10
		release := make(chan struct{})
		results := make(chan int, requests)
		for range requests {
			go func() {
				r := httptest.NewRequest(http.MethodPost, "/auth/local", nil)
				r.Body = &blockedPasswordBody{release: release, body: strings.NewReader(`{"user":"admin","password":"wrong"}`)}
				r.RemoteAddr = "192.0.2.1:1234"
				r.Header.Set("X-Kritika", "1")
				r.Header.Set("Origin", "https://kritika.example.com")
				w := httptest.NewRecorder()
				mux.ServeHTTP(w, r)
				results <- w.Code
			}()
		}
		synctest.Wait()
		close(release)
		counts := map[int]int{}
		for range requests {
			counts[<-results]++
		}
		if counts[http.StatusUnauthorized] != maxFailedSignIns || counts[http.StatusTooManyRequests] != requests-maxFailedSignIns {
			t.Fatalf("concurrent sign-in responses = %v, want %d checked and %d limited", counts, maxFailedSignIns, requests-maxFailedSignIns)
		}
	})
}
