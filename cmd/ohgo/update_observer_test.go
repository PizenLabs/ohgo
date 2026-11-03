package main

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/PizenLabs/ohgo/internal/config"
	"github.com/PizenLabs/ohgo/internal/update"
)

// fakeUpdateObservation is a release observer with no network and no cache.
// The test controls when Check returns, so startup latency and failure
// behaviour are observable rather than inferred.
type fakeUpdateObservation struct {
	mu          sync.Mutex
	status      update.Status
	checkStatus update.Status
	checkErr    error
	check       func(context.Context) (update.Status, error)
	checks      int
}

func (f *fakeUpdateObservation) Observe() update.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status
}

func (f *fakeUpdateObservation) Check(ctx context.Context) (update.Status, error) {
	f.mu.Lock()
	f.checks++
	check := f.check
	f.mu.Unlock()
	if check != nil {
		return check(ctx)
	}
	return f.checkStatus, f.checkErr
}

func (f *fakeUpdateObservation) checkCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checks
}

func availableStatus(latest string) update.Status {
	return update.Status{
		CurrentVersion:    "v1.0.0",
		LatestVersion:     latest,
		Available:         true,
		Reason:            update.ReasonAvailable,
		PlatformSupported: true,
		Cached:            true,
	}
}

// A cached observation must reach the UI before the first frame, and a slow
// refresh must never delay the caller that started the observer.
func TestObserveUpdatePublishesCachedHintWithoutWaiting(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	cached := availableStatus("v1.1.0")
	cached.Stale = true
	svc := &fakeUpdateObservation{
		status: cached,
		check: func(context.Context) (update.Status, error) {
			close(started)
			<-release
			return availableStatus("v1.2.0"), nil
		},
	}
	published := make(chan string, 2)
	start := time.Now()
	observeUpdate(context.Background(), "v1.0.0", svc, time.Second, func(v string) {
		published <- v
	})
	if elapsed := time.Since(start); elapsed > 100*time.Millisecond {
		t.Fatalf("observer blocked the startup path for %s", elapsed)
	}
	select {
	case got := <-published:
		if got != "v1.1.0" {
			t.Fatalf("first hint = %q, want the cached release", got)
		}
	case <-time.After(time.Second):
		t.Fatal("cached availability was not published")
	}

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background refresh never started")
	}
	close(release)
	select {
	case got := <-published:
		if got != "v1.2.0" {
			t.Fatalf("refreshed hint = %q, want the newer release", got)
		}
	case <-time.After(time.Second):
		t.Fatal("refreshed availability was not published")
	}
}

// A fresh cache is the whole point of the TTL: the UI redraws, sessions open,
// and prompts run without another request.
func TestObserveUpdateFreshCacheSkipsTheNetwork(t *testing.T) {
	status := availableStatus("v1.1.0")
	status.Stale = false
	svc := &fakeUpdateObservation{status: status}
	observeUpdate(context.Background(), "v1.0.0", svc, time.Second, func(string) {})
	time.Sleep(30 * time.Millisecond)
	if got := svc.checkCount(); got != 0 {
		t.Fatalf("fresh cache performed %d background checks", got)
	}
}

// A release hint is announced once per release. Repeated prompt boundaries
// must not repeat the same line.
func TestObserveUpdateAnnouncesEachReleaseOnce(t *testing.T) {
	status := availableStatus("v1.1.0")
	status.Stale = false
	svc := &fakeUpdateObservation{status: status}
	var mu sync.Mutex
	var published []string
	alert := observeUpdate(context.Background(), "v1.0.0", svc, time.Second, func(v string) {
		mu.Lock()
		published = append(published, v)
		mu.Unlock()
	})
	for i := 0; i < 20; i++ {
		alert(nil)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(published) != 1 || published[0] != "v1.1.0" {
		t.Fatalf("published = %v, want exactly one hint", published)
	}
}

// Background discovery is optional infrastructure: a failed refresh must stay
// silent and must not turn into a second UI message.
func TestObserveUpdateFailureStaysQuiet(t *testing.T) {
	done := make(chan struct{})
	cached := availableStatus("v1.1.0")
	cached.Stale = true
	svc := &fakeUpdateObservation{
		status: cached,
		check: func(context.Context) (update.Status, error) {
			close(done)
			return update.Status{}, errors.New("network unavailable")
		},
	}
	var mu sync.Mutex
	var published []string
	observeUpdate(context.Background(), "v1.0.0", svc, time.Second, func(v string) {
		mu.Lock()
		published = append(published, v)
		mu.Unlock()
	})
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("background refresh never finished")
	}
	time.Sleep(20 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(published) != 1 || published[0] != "v1.1.0" {
		t.Fatalf("published = %v, want only the cached hint", published)
	}
}

// A stuck endpoint is bounded by the check timeout, not by the process
// lifetime.
func TestObserveUpdateBoundsAStuckCheck(t *testing.T) {
	done := make(chan struct{})
	svc := &fakeUpdateObservation{
		status: update.Status{CurrentVersion: "v1.0.0", PlatformSupported: true},
		check: func(ctx context.Context) (update.Status, error) {
			<-ctx.Done()
			close(done)
			return update.Status{}, ctx.Err()
		},
	}
	start := time.Now()
	observeUpdate(context.Background(), "v1.0.0", svc, 25*time.Millisecond, nil)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("background check ignored its timeout")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("background check took %s, want a bounded timeout", elapsed)
	}
}

// Quitting the UI cancels the observer through the program lifetime context.
func TestObserveUpdateStopsWhenLifetimeEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	svc := &fakeUpdateObservation{
		status: update.Status{CurrentVersion: "v1.0.0", PlatformSupported: true},
		check: func(ctx context.Context) (update.Status, error) {
			<-ctx.Done()
			close(done)
			return update.Status{}, ctx.Err()
		},
	}
	observeUpdate(ctx, "v1.0.0", svc, time.Minute, nil)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("background check survived the program lifetime")
	}
}

// A disabled automatic check must not even construct an observer, regardless
// of whether stdout happens to be a terminal.
func TestStartUpdateObserverHonorsDisabledChecks(t *testing.T) {
	t.Setenv("OHGO_UPDATE_CHECK", "off")
	if got := startUpdateObserver(config.Settings{}, "v1.0.0", nil); got != nil {
		t.Fatal("disabled update checks still returned an observer")
	}
}

// The plain-REPL notification names the explicit command instead of implying
// that discovery installed anything.
func TestAnnounceUpdateNamesTheExplicitCommand(t *testing.T) {
	var out bytes.Buffer
	announceUpdate(&out, "v1.0.0", "v1.1.0")
	want := "A newer ohgo release is available: v1.0.0 → v1.1.0\nRun `ohgo update` to install.\n"
	if got := out.String(); got != want {
		t.Fatalf("announcement = %q, want %q", got, want)
	}
}
