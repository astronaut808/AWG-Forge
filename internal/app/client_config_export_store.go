package app

import (
	"errors"
	"sync"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/astronaut808/awg-forge/internal/controlauth"
	"github.com/astronaut808/awg-forge/internal/sqldb"
	"github.com/google/uuid"
)

var ErrClientConfigExportUnavailable = errors.New("client config export unavailable")

const maxClientConfigExports = 16

// Separate from Service.mu: cancellation/timers can wake a waiting download
// without waiting for the state lock. Lock order is Service.mu, then store.mu.
// The zero value is usable. No token, config or result is persisted.
type clientConfigExportStore struct {
	mu   sync.Mutex
	jobs map[string]*clientConfigExportJob
}

type clientConfigExportJob struct {
	metadata  controlapi.ClientConfigExport
	request   controlapi.ClientConfigExportRequest
	owner     controlauth.Digest
	authority sqldb.ClientConfigExportAuthority
	delivered bool
	claimed   bool
	ready     bool
	config    []byte
	done      chan struct{}
	signaled  bool
	timer     *time.Timer
}

func (store *clientConfigExportStore) create(node string, r controlapi.ClientConfigExportRequest, owner controlauth.Digest, authority sqldb.ClientConfigExportAuthority, now time.Time) (controlapi.ClientConfigExport, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.jobs == nil {
		store.jobs = make(map[string]*clientConfigExportJob)
	}
	for _, job := range store.jobs {
		if !now.Before(job.metadata.ExpiresAt) {
			store.removeLocked(job)
		}
	}
	if len(store.jobs) >= maxClientConfigExports {
		return controlapi.ClientConfigExport{}, sqldb.ErrOperationLimit
	}
	for _, job := range store.jobs {
		if job.metadata.NodeID == node {
			return controlapi.ClientConfigExport{}, sqldb.ErrOperationLimit
		}
	}
	job := &clientConfigExportJob{metadata: controlapi.ClientConfigExport{ExportID: uuid.NewString(), NodeID: node, TunnelID: r.TunnelID, ClientID: r.ClientID, ExpiresAt: now.Add(controlapi.ClientConfigExportTTL)}, request: r, owner: owner, authority: authority, done: make(chan struct{})}
	store.jobs[job.metadata.ExportID] = job
	job.timer = time.AfterFunc(time.Until(job.metadata.ExpiresAt), func() {
		store.mu.Lock()
		defer store.mu.Unlock()
		store.removeLocked(job)
	})
	return job.metadata, nil
}

// snapshot returns only immutable metadata/authority, never the secret buffer.
func (store *clientConfigExportStore) snapshot(node, id string) (*clientConfigExportJob, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	job := store.jobs[id]
	if job == nil || job.metadata.NodeID != node {
		return nil, ErrClientConfigExportUnavailable
	}
	if !time.Now().Before(job.metadata.ExpiresAt) {
		store.removeLocked(job)
		return nil, ErrClientConfigExportUnavailable
	}
	return job, nil
}

func (store *clientConfigExportStore) pending(node string) *clientConfigExportJob {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, job := range store.jobs {
		if job.metadata.NodeID == node && !job.delivered && time.Now().Before(job.metadata.ExpiresAt) {
			return job
		}
	}
	return nil
}

func (store *clientConfigExportStore) deliver(job *clientConfigExportJob) (*controlapi.ClientConfigExportDelivery, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.jobs[job.metadata.ExportID] != job || job.delivered || !time.Now().Before(job.metadata.ExpiresAt) {
		return nil, ErrClientConfigExportUnavailable
	}
	job.delivered = true
	return &controlapi.ClientConfigExportDelivery{ClientConfigExportLocator: controlapi.ClientConfigExportLocator{ExportID: job.metadata.ExportID}, Request: job.request, Authority: job.authority.Poll, ExpiresAt: job.metadata.ExpiresAt}, nil
}

func (store *clientConfigExportStore) complete(job *clientConfigExportJob, result controlapi.ClientConfigExportResult) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.jobs[job.metadata.ExportID] != job || !job.delivered || job.ready || !time.Now().Before(job.metadata.ExpiresAt) || result.Validate() != nil {
		return ErrClientConfigExportUnavailable
	}
	job.ready = true
	if result.Status == "ready" {
		job.config = []byte(result.Config)
	}
	store.signalLocked(job)
	return nil
}

func (store *clientConfigExportStore) claim(job *clientConfigExportJob, owner controlauth.Digest) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.jobs[job.metadata.ExportID] != job || job.owner != owner || job.claimed || !time.Now().Before(job.metadata.ExpiresAt) {
		return ErrClientConfigExportUnavailable
	}
	job.claimed = true
	return nil
}

func (store *clientConfigExportStore) consume(job *clientConfigExportJob, owner controlauth.Digest) ([]byte, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.jobs[job.metadata.ExportID] != job || job.owner != owner || !job.claimed || !job.ready || !time.Now().Before(job.metadata.ExpiresAt) || len(job.config) == 0 {
		return nil, ErrClientConfigExportUnavailable
	}
	config := job.config
	job.config = nil // Transfer ownership; caller wipes after writing the response.
	store.removeLocked(job)
	return config, nil
}

func (store *clientConfigExportStore) cancel(job *clientConfigExportJob) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.removeLocked(job)
}

func (store *clientConfigExportStore) clear() {
	store.mu.Lock()
	defer store.mu.Unlock()
	for _, job := range store.jobs {
		store.removeLocked(job)
	}
}

func (store *clientConfigExportStore) signalLocked(job *clientConfigExportJob) {
	if !job.signaled {
		close(job.done)
		job.signaled = true
	}
}

func (store *clientConfigExportStore) removeLocked(job *clientConfigExportJob) {
	// A late timer from a removed entry cannot remove its replacement.
	if store.jobs[job.metadata.ExportID] != job {
		return
	}
	delete(store.jobs, job.metadata.ExportID)
	if job.timer != nil {
		job.timer.Stop()
	}
	clear(job.config)
	job.config = nil
	store.signalLocked(job)
}
