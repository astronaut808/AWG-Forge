package storage

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlpki"
	"github.com/google/uuid"
)

const ControlServerRotationJournalFileName = ".control-server-rotation.json"

// Version 1 records intent only; no keys or certificate payloads enter a journal.
type ControlServerRotationJournal struct {
	Version             int       `json:"version"`
	ControllerID        string    `json:"controller_id"`
	CAGeneration        string    `json:"ca_generation"`
	OldServerGeneration string    `json:"old_server_generation"`
	NewServerGeneration string    `json:"new_server_generation"`
	StartedAt           time.Time `json:"started_at"`
}

func (j ControlServerRotationJournal) validate() error {
	id, err := uuid.Parse(j.ControllerID)
	if err != nil || id.String() != j.ControllerID || j.Version != 1 || j.StartedAt.IsZero() || j.OldServerGeneration == j.NewServerGeneration {
		return errors.New("invalid control server rotation journal")
	}
	for _, g := range []string{j.CAGeneration, j.OldServerGeneration, j.NewServerGeneration} {
		if err := ValidateControlGeneration(g); err != nil {
			return err
		}
	}
	return nil
}

func (s Store) ControlServerRotationJournalPath() string {
	return filepath.Join(s.dir, ControlServerRotationJournalFileName)
}

// CheckNoControlServerRotation is a fence even for malformed or unsafe journals.
func (s Store) CheckNoControlServerRotation() error {
	if _, err := os.Lstat(s.ControlServerRotationJournalPath()); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return errors.New("control server rotation requires recovery")
}

func (s Store) CheckControlServerGeneration(generation string) error {
	if err := ValidateControlGeneration(generation); err != nil {
		return err
	}
	for _, p := range []string{s.dir, filepath.Join(s.dir, "control"), filepath.Join(s.dir, "control", "server")} {
		if err := privateDir(p); err != nil {
			return err
		}
	}
	_, err := os.Lstat(filepath.Join(s.dir, "control", "server", generation))
	if !errors.Is(err, os.ErrNotExist) {
		return errors.New("control server generation already exists or is unsafe")
	}
	return nil
}

func rotationStep(hook func(string) error, step string) error {
	if hook != nil {
		return hook(step)
	}
	return nil
}

func writeControlRotationFile(path string, body []byte, hook func(string) error, label string) error {
	if err := rotationStep(hook, "before-write:"+label); err != nil {
		return err
	}
	out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	if _, err := out.Write(body); err != nil {
		return err
	}
	if err := rotationStep(hook, "after-write:"+label); err != nil {
		return err
	}
	if err := rotationStep(hook, "before-sync:"+label); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := rotationStep(hook, "after-sync:"+label); err != nil {
		return err
	}
	if err := syncRestoreDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	return rotationStep(hook, "after-directory-sync:"+label)
}

func (s Store) SaveControlServerRotationJournal(j ControlServerRotationJournal, hook func(string) error) error {
	if err := j.validate(); err != nil {
		return err
	}
	if err := privateDir(s.dir); err != nil {
		return err
	}
	body, err := json.Marshal(j)
	if err != nil {
		return err
	}
	return writeControlRotationFile(s.ControlServerRotationJournalPath(), append(body, '\n'), hook, "journal")
}

func (s Store) LoadControlServerRotationJournal() (ControlServerRotationJournal, error) {
	var j ControlServerRotationJournal
	path := s.ControlServerRotationJournalPath()
	if _, err := os.Lstat(path); err != nil {
		return j, err
	}
	if err := privateDir(s.dir); err != nil {
		return j, err
	}
	if err := privateFile(path); err != nil {
		return j, err
	}
	f, err := os.Open(path)
	if err != nil {
		return j, err
	}
	defer func() { _ = f.Close() }()
	body, err := io.ReadAll(io.LimitReader(f, 4097))
	if err != nil {
		return j, err
	}
	if len(body) > 4096 {
		return j, errors.New("invalid control server rotation journal size")
	}
	// Reject duplicate keys as well as unknown fields, versions and trailing JSON.
	d := json.NewDecoder(bytes.NewReader(body))
	tok, err := d.Token()
	if err != nil || tok != json.Delim('{') {
		return j, errors.New("invalid control server rotation journal")
	}
	seen := map[string]bool{}
	for d.More() {
		tok, err := d.Token()
		if err != nil {
			return j, errors.New("invalid control server rotation journal")
		}
		key, ok := tok.(string)
		if !ok || seen[key] || !rotationJournalKey(key) {
			return j, errors.New("invalid control server rotation journal")
		}
		seen[key] = true
		var v json.RawMessage
		if err := d.Decode(&v); err != nil {
			return j, errors.New("invalid control server rotation journal")
		}
	}
	d = json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if err := d.Decode(&j); err != nil {
		return j, errors.New("invalid control server rotation journal")
	}
	var trailing any
	if err := d.Decode(&trailing); !errors.Is(err, io.EOF) {
		return j, errors.New("invalid control server rotation journal")
	}
	return j, j.validate()
}

func rotationJournalKey(key string) bool {
	switch key {
	case "version", "controller_id", "ca_generation", "old_server_generation", "new_server_generation", "started_at":
		return true
	default:
		return false
	}
}

func (s Store) DeleteControlServerRotationJournal(hook func(string) error) error {
	if err := privateDir(s.dir); err != nil {
		return err
	}
	if err := privateFile(s.ControlServerRotationJournalPath()); err != nil {
		return err
	}
	if err := rotationStep(hook, "before-journal-delete"); err != nil {
		return err
	}
	if err := os.Remove(s.ControlServerRotationJournalPath()); err != nil {
		return err
	}
	if err := rotationStep(hook, "after-journal-delete"); err != nil {
		return err
	}
	if err := s.SyncStateDirectory(); err != nil {
		return err
	}
	return rotationStep(hook, "after-journal-delete-sync")
}

func (s Store) WriteControlServerGeneration(j ControlServerRotationJournal, m controlpki.Material, hook func(string) error) error {
	if err := j.validate(); err != nil {
		return err
	}
	if err := s.CheckControlServerGeneration(j.NewServerGeneration); err != nil {
		return err
	}
	parent := filepath.Join(s.dir, "control", "server")
	dir := filepath.Join(parent, j.NewServerGeneration)
	if err := rotationStep(hook, "before-generation"); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		return err
	}
	if err := rotationStep(hook, "after-generation"); err != nil {
		return err
	}
	if err := syncRestoreDirectory(parent); err != nil {
		return err
	}
	if err := rotationStep(hook, "after-generation-sync"); err != nil {
		return err
	}
	for _, f := range []struct {
		name string
		body []byte
	}{{controlpki.ServerKeyFile, m.ServerKey}, {controlpki.ServerCertFile, m.ServerCert}} {
		if err := writeControlRotationFile(filepath.Join(dir, f.name), f.body, hook, f.name); err != nil {
			return err
		}
	}
	return nil
}

// RemoveControlServerGeneration removes exactly a journal-owned server pair.
// Interrupted unlink is retryable, including syncing an already absent directory.
func (s Store) RemoveControlServerGeneration(j ControlServerRotationJournal, generation string, hook func(string) error) error {
	if err := j.validate(); err != nil {
		return err
	}
	if generation != j.OldServerGeneration && generation != j.NewServerGeneration {
		return errors.New("server generation is not journal-owned")
	}
	for _, p := range []string{s.dir, filepath.Join(s.dir, "control"), filepath.Join(s.dir, "control", "server")} {
		if err := privateDir(p); err != nil {
			return err
		}
	}
	parent := filepath.Join(s.dir, "control", "server")
	dir := filepath.Join(parent, generation)
	if _, err := os.Lstat(dir); errors.Is(err, os.ErrNotExist) {
		return syncRestoreDirectory(parent)
	} else if err != nil {
		return err
	}
	if err := privateDir(dir); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() != controlpki.ServerKeyFile && e.Name() != controlpki.ServerCertFile {
			return errors.New("unexpected control server generation entry")
		}
		if err := privateFile(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	for _, name := range []string{controlpki.ServerKeyFile, controlpki.ServerCertFile} {
		if err := rotationStep(hook, "before-retire:"+name); err != nil {
			return err
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := rotationStep(hook, "after-retire:"+name); err != nil {
			return err
		}
	}
	if err := syncRestoreDirectory(dir); err != nil {
		return err
	}
	if err := rotationStep(hook, "before-retire-directory"); err != nil {
		return err
	}
	if err := os.Remove(dir); err != nil {
		return err
	}
	if err := rotationStep(hook, "after-retire-directory"); err != nil {
		return err
	}
	if err := syncRestoreDirectory(parent); err != nil {
		return err
	}
	return rotationStep(hook, "after-retire-sync")
}
