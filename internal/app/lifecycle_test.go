package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

// A listener can take time to return after Close; no TCP port is needed here.
type drainingListener struct {
	closed, release, entered chan struct{}
	once                     sync.Once
}

func (l *drainingListener) Accept() (net.Conn, error) {
	close(l.entered)
	<-l.closed
	<-l.release
	return nil, net.ErrClosed
}
func (l *drainingListener) Close() error   { l.once.Do(func() { close(l.closed) }); return nil }
func (l *drainingListener) Addr() net.Addr { return &net.TCPAddr{} }

func assertStillDraining(t *testing.T, a *App) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := a.WaitOwnedTasks(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unfinished work reported drained: %v", err)
	}
	if err := a.Store.DB.Ping(); err != nil {
		t.Fatal("storage closed before work finished", err)
	}
}
func assertDrained(t *testing.T, a *App) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := a.WaitOwnedTasks(ctx); err != nil {
		t.Fatal("owned work did not drain", err)
	}
}

func TestShutdownDrainsOwnedTasks(t *testing.T) {
	t.Run("refresh_without_request", func(t *testing.T) {
		started, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
		var releaseOnce sync.Once
		unblock := func() { releaseOnce.Do(func() { close(release) }) }
		a := contractApp(t, func(r *http.Request) (*http.Response, error) {
			close(started)
			<-r.Context().Done()
			close(cancelled)
			<-release
			return nil, r.Context().Err()
		})
		defer unblock()
		src := expiredContractSource(t, a)
		waiter, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			a.mu.Lock()
			defer a.mu.Unlock()
			_, _ = a.subscriptionCredential(waiter, &src)
		}()
		<-started
		cancel()
		<-done // No HTTP handler or request waiter remains.
		a.CloseAdmission()
		<-cancelled
		assertStillDraining(t, a)
		unblock()
		assertDrained(t, a)
		current, err := a.Store.source(src.ID)
		if err != nil || current.AuthStatus != "needs_reauth" {
			t.Fatalf("refresh result not persisted before drain: %s %v", current.AuthStatus, err)
		}
		a.mu.Lock()
		_, err = a.subscriptionCredential(context.Background(), &current)
		a.mu.Unlock()
		if err == nil {
			t.Fatal("refresh admitted after stop")
		}
	})
	t.Run("idle_login_listener", func(t *testing.T) {
		a := contractApp(t, nil)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		listener := &drainingListener{closed: make(chan struct{}), release: make(chan struct{}), entered: make(chan struct{})}
		var once sync.Once
		release := func() { once.Do(func() { close(listener.release) }) }
		defer release()
		op := &Login{Status: "pending", Expires: time.Now().Add(time.Minute), cancel: cancel}
		a.mu.Lock()
		source, _ := a.Store.source("source")
		a.logins[source.AccountID] = op
		op.accountGeneration = source.AccountGeneration
		a.serveLogin(ctx, cancel, "source", op, &http.Server{}, []net.Listener{listener})
		a.mu.Unlock()
		<-listener.entered
		a.CloseAdmission()
		<-listener.closed
		assertStillDraining(t, a)
		release()
		assertDrained(t, a)
		a.mu.Lock()
		status := op.Status
		a.mu.Unlock()
		if status != "expired" {
			t.Fatalf("login cleanup unfinished: %s", status)
		}
	})
	t.Run("callback_finishes_after_cancellation", func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		unblock := func() { once.Do(func() { close(release) }) }
		a := contractApp(t, func(r *http.Request) (*http.Response, error) { close(entered); <-release; return nil, context.Canceled })
		defer unblock()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		op := &Login{Status: "pending", Expires: time.Now().Add(time.Minute), state: "state", cancel: cancel, version: 1, generation: 1}
		source, _ := a.Store.source("source")
		a.logins[source.AccountID] = op
		op.accountGeneration = source.AccountGeneration
		callback, _ := url.Parse("http://localhost:1455/auth/callback")
		done := make(chan struct{})
		go func() {
			defer close(done)
			a.oauthCallback(httptest.NewRecorder(), httptest.NewRequest("GET", callback.String()+"?state=state&code=synthetic", nil), ctx, "source", op, callback)
		}()
		<-entered
		assertStillDraining(t, a)
		unblock()
		assertDrained(t, a)
		<-done
	})
	t.Run("maintenance_and_late_admission", func(t *testing.T) {
		a := contractApp(t, nil)
		a.StartMaintenance(context.Background())
		assertDrained(t, a)
		a.StartMaintenance(context.Background()) // No new owned work after the barrier.
		w := compatCall(a, "/v1/messages", compatMessagesBody, "test-client-key")
		if w.Code != 503 {
			t.Fatal("request admitted after shutdown")
		}
		assertDrained(t, a)
	})
	t.Run("http_handler_after_forced_cancel", func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		unblock := func() { once.Do(func() { close(release) }) }
		a := contractApp(t, func(r *http.Request) (*http.Response, error) {
			close(entered)
			<-r.Context().Done()
			<-release
			return nil, r.Context().Err()
		})
		defer unblock()
		done := make(chan struct{})
		go func() { defer close(done); compatCall(a, "/v1/responses", contractBody, "test-client-key") }()
		<-entered
		a.CloseAdmission()
		a.CancelAll()
		assertStillDraining(t, a)
		unblock()
		assertDrained(t, a)
		<-done
		if record := waitRecords(t, a, 1)[0]; record.Status != "cancelled" || record.Ended == nil {
			t.Fatal("request record unfinished at drain")
		}
	})
}
