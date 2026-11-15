package app

import (
	"sync"
	"testing"
	"time"
)

func TestRelayProbesBoundConcurrency(t *testing.T) {
	ctx := t.Context()
	started := make(chan struct{}, 13)
	release := make(chan struct{})
	var unblock sync.Once
	defer unblock.Do(func() { close(release) })

	probes := make([]func(), cap(started))
	for i := range probes {
		probes[i] = func() {
			started <- struct{}{}
			<-release
		}
	}
	done := make(chan struct{})
	go func() {
		runRelayProbes(ctx, probes)
		close(done)
	}()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for range relayCheckWorkers {
		select {
		case <-started:
		case <-timeout.C:
			t.Fatal("probes did not run concurrently")
		}
	}
	select {
	case <-started:
		t.Fatal("concurrency limit exceeded")
	default:
	}
	unblock.Do(func() { close(release) })
	select {
	case <-done:
		if len(started) != len(probes)-relayCheckWorkers {
			t.Fatal("not all relays were checked")
		}
	case <-timeout.C:
		t.Fatal("probes did not finish")
	}
}

func TestRelayStaleThresholdAllowsFullSweep(t *testing.T) {
	if got := relayStaleAfter(13); got != 3*time.Minute {
		t.Fatalf("stale threshold for 13 relays: %v", got)
	}
	if got := relayStaleAfter(100); got <= 25*(relayProbeTimeout+relayProbeCleanup) {
		t.Fatalf("stale threshold shorter than a full sweep: %v", got)
	}
}

func TestInterruptedRelayProbePreservesLastResult(t *testing.T) {
	var checks relayChecks
	target := relayCheckTarget{tunnelID: "tunnel"}
	checks.record(target, "reachable")
	old := checks.results[target]
	checks.record(target, relayProbeInterrupted)
	if got := checks.results[target]; got != old {
		t.Fatalf("interrupted probe changed previous result: %+v", got)
	}
}
