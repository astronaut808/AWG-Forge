package storage

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

const NodeRenewalJournalFileName = ".node-certificate-renewal.json"

// NodeRenewalJournal contains public fencing metadata only. Candidate key and
// exact signed CSR are immutable private files, durable before any HTTP request.
type NodeRenewalJournal struct {
	PredecessorGeneration string `json:"predecessor_generation"`
	Generation            string `json:"generation"`
	ControllerID          string `json:"controller_id"`
	NodeID                string `json:"node_id"`
	BindingEpoch          uint64 `json:"binding_epoch"`
	StateEpoch            string `json:"state_epoch"`
	ControllerURL         string `json:"controller_url"`
	CAPin                 string `json:"ca_pin"`
}

type NodeRenewalCandidate struct{ PrivateKey, CSR []byte }

func (s Store) NodeRenewalJournalPath() string {
	return filepath.Join(s.dir, NodeRenewalJournalFileName)
}

func validateNodeRenewalJournal(j NodeRenewalJournal) error {
	if ValidateControlGeneration(j.Generation) != nil || ValidateControlGeneration(j.PredecessorGeneration) != nil || j.Generation == j.PredecessorGeneration || j.BindingEpoch == 0 || j.ControllerURL == "" || j.CAPin == "" {
		return errors.New("invalid node renewal journal")
	}
	for _, id := range []string{j.ControllerID, j.NodeID, j.StateEpoch} {
		parsed, err := uuid.Parse(id)
		if err != nil || parsed.String() != id {
			return errors.New("invalid node renewal journal")
		}
	}
	return nil
}

// SaveNodeRenewalJournal requires the state mutation lock. The journal becomes
// visible only after the private temporary file is complete and synchronized.
func (s Store) SaveNodeRenewalJournal(j NodeRenewalJournal) error {
	return s.saveNodeRenewalJournal(j, nil)
}

func (s Store) saveNodeRenewalJournal(j NodeRenewalJournal, hook func(string) error) error {
	if err := validateNodeRenewalJournal(j); err != nil {
		return err
	}
	if err := privateDir(s.dir); err != nil {
		return err
	}
	body, err := json.Marshal(j)
	if err != nil {
		return err
	}
	path := s.NodeRenewalJournalPath()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return errors.New("node renewal journal already exists or is unsafe")
	}
	tmp, err := os.CreateTemp(s.dir, ".node-renewal-journal-*")
	if err != nil {
		return err
	}
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
	}()
	if err := rotationStep(hook, "created"); err != nil {
		return err
	}
	if _, err := tmp.Write(body); err != nil {
		return err
	}
	if err := rotationStep(hook, "written"); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := rotationStep(hook, "synced"); err != nil {
		return err
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return errors.New("node renewal journal already exists or is unsafe")
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	if err := rotationStep(hook, "published"); err != nil {
		return err
	}
	return syncRestoreDirectory(s.dir)
}

func (s Store) LoadNodeRenewalJournal() (NodeRenewalJournal, error) {
	if _, err := os.Lstat(s.NodeRenewalJournalPath()); err != nil {
		return NodeRenewalJournal{}, err
	}
	body, err := readNodePrivateFile(s.NodeRenewalJournalPath(), 4096)
	if err != nil {
		return NodeRenewalJournal{}, err
	}
	var j NodeRenewalJournal
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if err := d.Decode(&j); err != nil {
		return j, errors.New("invalid node renewal journal")
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return j, errors.New("invalid node renewal journal")
	}
	return j, validateNodeRenewalJournal(j)
}

func (s Store) DeleteNodeRenewalJournal() error {
	if _, err := readNodePrivateFile(s.NodeRenewalJournalPath(), 4096); err != nil {
		return err
	}
	if err := os.Remove(s.NodeRenewalJournalPath()); err != nil {
		return err
	}
	return syncRestoreDirectory(s.dir)
}

func writeNodeRenewalFile(path string, body []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(body)
	syncErr := f.Sync()
	closeErr := f.Close()
	return errors.Join(writeErr, syncErr, closeErr)
}

func (s Store) SaveNodeRenewalCandidate(generation string, candidate NodeRenewalCandidate) error {
	if ValidateControlGeneration(generation) != nil || len(candidate.PrivateKey) == 0 || len(candidate.PrivateKey) > 8192 || len(candidate.CSR) == 0 || len(candidate.CSR) > 8192 {
		return errors.New("invalid renewal candidate")
	}
	base := filepath.Join(s.dir, "node-renewal")
	for _, dir := range []string{s.dir, base} {
		if err := ensurePrivateDir(dir); err != nil {
			return err
		}
	}
	dir := filepath.Join(base, generation)
	if err := os.Mkdir(dir, 0700); err != nil {
		return err
	}
	if err := writeNodeRenewalFile(filepath.Join(dir, "key.pem"), candidate.PrivateKey); err != nil {
		return err
	}
	if err := writeNodeRenewalFile(filepath.Join(dir, "csr.der"), candidate.CSR); err != nil {
		return err
	}
	for _, dir := range []string{dir, base, s.dir} {
		if err := syncRestoreDirectory(dir); err != nil {
			return err
		}
	}
	return nil
}

func (s Store) LoadNodeRenewalCandidate(generation string) (NodeRenewalCandidate, error) {
	if err := ValidateControlGeneration(generation); err != nil {
		return NodeRenewalCandidate{}, err
	}
	dir := filepath.Join(s.dir, "node-renewal", generation)
	for _, path := range []string{s.dir, filepath.Dir(dir), dir} {
		if err := privateDir(path); err != nil {
			return NodeRenewalCandidate{}, err
		}
	}
	key, err := readNodePrivateFile(filepath.Join(dir, "key.pem"), 8192)
	if err != nil {
		return NodeRenewalCandidate{}, err
	}
	csr, err := readNodePrivateFile(filepath.Join(dir, "csr.der"), 8192)
	if err != nil {
		return NodeRenewalCandidate{}, err
	}
	return NodeRenewalCandidate{PrivateKey: key, CSR: csr}, nil
}

// PublishRenewedNodeIdentity exposes a complete immutable directory in one
// rename. An interrupted write cannot occupy the journal's target generation.
func (s Store) PublishRenewedNodeIdentity(generation string, material NodeIdentityMaterial) error {
	if err := ValidateControlGeneration(generation); err != nil {
		return err
	}
	final := filepath.Join(s.dir, "node", generation)
	if _, err := os.Lstat(final); err == nil {
		existing, err := s.LoadNodeIdentity(generation)
		if err != nil || !bytes.Equal(existing.CACert, material.CACert) || !bytes.Equal(existing.Certificate, material.Certificate) || !bytes.Equal(existing.PrivateKey, material.PrivateKey) {
			return errors.New("conflicting renewed identity")
		}
		return syncRestoreDirectory(filepath.Dir(final))
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporaryID, err := uuid.NewRandom()
	if err != nil {
		return err
	}
	temporaryGeneration := strings.ReplaceAll(temporaryID.String(), "-", "")
	if err := s.SaveNodeIdentity(temporaryGeneration, material); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(s.dir, "node", temporaryGeneration), final); err != nil {
		return err
	}
	if err := syncRestoreDirectory(filepath.Dir(final)); err != nil {
		return err
	}
	return syncRestoreDirectory(s.dir)
}
