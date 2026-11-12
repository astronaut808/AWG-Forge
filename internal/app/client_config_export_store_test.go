package app

import (
	"bytes"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/astronaut808/awg-forge/internal/controlauth"
	"github.com/astronaut808/awg-forge/internal/sqldb"
	"github.com/google/uuid"
)

func storeExportFixture(t *testing.T, store *clientConfigExportStore, node string, now time.Time) (*clientConfigExportJob, controlauth.Digest) {
	t.Helper()
	owner := controlauth.Digest{1}
	r := controlapi.ClientConfigExportRequest{StateEpoch: uuid.NewString(), BindingEpoch: 1, TunnelID: "t", ClientID: "c", TunnelRevision: 1}
	m, err := store.create(node, r, owner, sqldb.ClientConfigExportAuthority{}, now)
	if err != nil {
		t.Fatal(err)
	}
	job, err := store.snapshot(node, m.ExportID)
	if err != nil {
		t.Fatal(err)
	}
	return job, owner
}

func TestClientConfigExportStoreOneUseConcurrentAndWipe(t *testing.T) {
	var store clientConfigExportStore
	defer store.clear()
	job, owner := storeExportFixture(t, &store, uuid.NewString(), time.Now())
	if store.claim(job, controlauth.Digest{2}) == nil {
		t.Fatal("foreign claim accepted")
	}
	var winners atomic.Int32
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			if store.claim(job, owner) == nil {
				winners.Add(1)
			}
		})
	}
	wg.Wait()
	if winners.Load() != 1 {
		t.Fatal("multiple consumers")
	}
	if _, err := store.deliver(job); err != nil {
		t.Fatal(err)
	}
	// Ready/failure messages still have a valid pinned wire authority.
	p := controlapi.OperationPoll{SessionID: uuid.NewString(), BootID: uuid.NewString(), BootSequence: 1, StateEpoch: job.request.StateEpoch, BindingEpoch: 1}
	r := controlapi.ClientConfigExportResult{ClientConfigExportLocator: controlapi.ClientConfigExportLocator{ExportID: job.metadata.ExportID}, Authority: p, Status: "ready", Config: "disposable-config"}
	if store.complete(job, r) != nil {
		t.Fatal("complete")
	}
	buffer := job.config
	if store.complete(job, r) == nil {
		t.Fatal("duplicate completion accepted")
	}
	value, err := store.consume(job, owner)
	if err != nil || string(value) != "disposable-config" {
		t.Fatal("consume")
	}
	clear(value)
	if !bytes.Equal(buffer, make([]byte, len(buffer))) {
		t.Fatal("transferred buffer not wiped")
	}
	if _, err := store.consume(job, owner); err == nil {
		t.Fatal("replay accepted")
	}
	store.cancel(job) // Old cancellation cannot remove a newly prepared job.
	next, _ := storeExportFixture(t, &store, job.metadata.NodeID, time.Now())
	store.cancel(job)
	if _, err := store.snapshot(next.metadata.NodeID, next.metadata.ExportID); err != nil {
		t.Fatal("old cancel removed replacement")
	}
	store.clear()
	select {
	case <-next.done:
	default:
		t.Fatal("shutdown did not wake consumer")
	}
}

func TestClientConfigExportStoreCapacityExpiryCancellation(t *testing.T) {
	var store clientConfigExportStore
	defer store.clear()
	job, owner := storeExportFixture(t, &store, uuid.NewString(), time.Now())
	if _, err := store.create(job.metadata.NodeID, job.request, owner, job.authority, time.Now()); err == nil {
		t.Fatal("node quota")
	}
	for range maxClientConfigExports - 1 {
		storeExportFixture(t, &store, uuid.NewString(), time.Now())
	}
	if _, err := store.create(uuid.NewString(), job.request, owner, job.authority, time.Now()); err == nil {
		t.Fatal("global quota")
	}
	store.clear()
	expiring, _ := storeExportFixture(t, &store, uuid.NewString(), time.Now().Add(-controlapi.ClientConfigExportTTL+100*time.Millisecond))
	select {
	case <-expiring.done:
	case <-time.After(2 * time.Second):
		t.Fatal("idle expiry did not wake")
	}
	if _, err := store.snapshot(expiring.metadata.NodeID, expiring.metadata.ExportID); err == nil {
		t.Fatal("expired retained")
	}
	ready, _ := storeExportFixture(t, &store, uuid.NewString(), time.Now())
	store.mu.Lock()
	ready.config = []byte("cancel-canary")
	buffer := ready.config
	store.mu.Unlock()
	store.cancel(ready)
	if !bytes.Equal(buffer, make([]byte, len(buffer))) {
		t.Fatal("cancel retained bytes")
	}
}
