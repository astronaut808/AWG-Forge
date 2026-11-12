package storage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/astronaut808/awg-forge/internal/controlpki"
)

const ControlIdentityJournalFileName = ".control-identity-preparation.json"

var generationRE = regexp.MustCompile(`^[0-9a-f]{32}$`)

type ControlIdentityJournal struct {
	ControllerID     string    `json:"controller_id"`
	CAGeneration     string    `json:"ca_generation"`
	ServerGeneration string    `json:"server_generation"`
	StartedAt        time.Time `json:"started_at"`
}

func ValidateControlGeneration(id string) error {
	if !generationRE.MatchString(id) {
		return errors.New("invalid control identity generation")
	}
	return nil
}

func ControlIdentityRelativePaths(caGeneration, serverGeneration string) ([]string, error) {
	if err := ValidateControlGeneration(caGeneration); err != nil {
		return nil, err
	}
	if err := ValidateControlGeneration(serverGeneration); err != nil {
		return nil, err
	}
	return []string{
		filepath.ToSlash(filepath.Join("control", "ca", caGeneration, controlpki.CAKeyFile)),
		filepath.ToSlash(filepath.Join("control", "ca", caGeneration, controlpki.CACertFile)),
		filepath.ToSlash(filepath.Join("control", "server", serverGeneration, controlpki.ServerKeyFile)),
		filepath.ToSlash(filepath.Join("control", "server", serverGeneration, controlpki.ServerCertFile)),
	}, nil
}

func (s Store) ControlIdentityJournalPath() string {
	return filepath.Join(s.dir, ControlIdentityJournalFileName)
}

func (s Store) SyncStateDirectory() error { return syncRestoreDirectory(s.dir) }

// CheckControlIdentityPath validates the complete managed tree before a journal
// is published and refuses to reuse either proposed immutable generation.
func (s Store) CheckControlIdentityPath(caGeneration, serverGeneration string) error {
	if _, err := ControlIdentityRelativePaths(caGeneration, serverGeneration); err != nil {
		return err
	}
	if err := privateDir(s.dir); err != nil {
		return err
	}
	for _, rel := range []string{"control", "control/ca", "control/server"} {
		path := filepath.Join(s.dir, filepath.FromSlash(rel))
		if err := privateDir(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	for _, rel := range []string{filepath.Join("control", "ca", caGeneration), filepath.Join("control", "server", serverGeneration)} {
		_, err := os.Lstat(filepath.Join(s.dir, rel))
		if err == nil {
			return errors.New("control identity generation already exists")
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (s Store) SaveControlIdentityJournal(journal ControlIdentityJournal) error {
	if journal.ControllerID == "" || journal.StartedAt.IsZero() {
		return errors.New("invalid control identity journal")
	}
	if _, err := ControlIdentityRelativePaths(journal.CAGeneration, journal.ServerGeneration); err != nil {
		return err
	}
	if err := privateDir(s.dir); err != nil {
		return err
	}
	body, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(s.ControlIdentityJournalPath(), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := file.Write(append(body, '\n'))
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
		return err
	}
	return syncRestoreDirectory(s.dir)
}

func (s Store) LoadControlIdentityJournal() (ControlIdentityJournal, error) {
	path := s.ControlIdentityJournalPath()
	// An absent optional journal must remain absent even when an older
	// installation uses a symlinked ancestor such as macOS /var.
	if _, err := os.Lstat(path); err != nil {
		return ControlIdentityJournal{}, err
	}
	if err := privateFile(path); err != nil {
		return ControlIdentityJournal{}, err
	}
	body, err := os.ReadFile(path)
	if err != nil {
		return ControlIdentityJournal{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var journal ControlIdentityJournal
	if err := decoder.Decode(&journal); err != nil {
		return ControlIdentityJournal{}, errors.New("invalid control identity journal")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) || journal.ControllerID == "" || journal.StartedAt.IsZero() {
		return ControlIdentityJournal{}, errors.New("invalid control identity journal")
	}
	if _, err := ControlIdentityRelativePaths(journal.CAGeneration, journal.ServerGeneration); err != nil {
		return ControlIdentityJournal{}, err
	}
	return journal, nil
}

func (s Store) DeleteControlIdentityJournal() error {
	if err := privateFile(s.ControlIdentityJournalPath()); err != nil {
		return err
	}
	if err := os.Remove(s.ControlIdentityJournalPath()); err != nil {
		return err
	}
	return syncRestoreDirectory(s.dir)
}

// WriteControlIdentity creates only the journal-owned immutable generations.
// afterSync is an optional test fault hook called after each durable step.
func (s Store) WriteControlIdentity(journal ControlIdentityJournal, material controlpki.Material, afterSync func(string) error) error {
	if _, err := ControlIdentityRelativePaths(journal.CAGeneration, journal.ServerGeneration); err != nil {
		return err
	}
	for _, part := range []struct{ dir, label string }{
		{s.dir, "config"},
		{filepath.Join(s.dir, "control"), "control"},
		{filepath.Join(s.dir, "control", "ca"), "ca"},
		{filepath.Join(s.dir, "control", "server"), "server"},
	} {
		if err := ensurePrivateDir(part.dir); err != nil {
			return err
		}
		if part.dir != s.dir {
			if err := syncRestoreDirectory(filepath.Dir(part.dir)); err != nil {
				return err
			}
		}
		if err := syncRestoreDirectory(part.dir); err != nil {
			return err
		}
		if afterSync != nil {
			if err := afterSync("directory:" + part.label); err != nil {
				return err
			}
		}
	}
	for _, generation := range []struct {
		kind, id  string
		key, cert []byte
	}{
		{"ca", journal.CAGeneration, material.CAKey, material.CACert},
		{"server", journal.ServerGeneration, material.ServerKey, material.ServerCert},
	} {
		parent := filepath.Join(s.dir, "control", generation.kind)
		dir := filepath.Join(parent, generation.id)
		if err := os.Mkdir(dir, 0700); err != nil {
			return err
		}
		if err := syncRestoreDirectory(parent); err != nil {
			return err
		}
		if afterSync != nil {
			if err := afterSync("generation:" + generation.kind); err != nil {
				return err
			}
		}
		for _, file := range []struct {
			name string
			body []byte
		}{{"key.pem", generation.key}, {"cert.pem", generation.cert}} {
			path := filepath.Join(dir, file.name)
			out, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return err
			}
			_, writeErr := out.Write(file.body)
			syncErr := out.Sync()
			closeErr := out.Close()
			if err := errors.Join(writeErr, syncErr, closeErr); err != nil {
				return err
			}
			if err := syncRestoreDirectory(dir); err != nil {
				return err
			}
			if afterSync != nil {
				if err := afterSync("file:" + generation.kind + ":" + file.name); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (s Store) LoadControlIdentity(caGeneration, serverGeneration string) (controlpki.Material, error) {
	paths, err := ControlIdentityRelativePaths(caGeneration, serverGeneration)
	if err != nil {
		return controlpki.Material{}, err
	}
	for _, dir := range []string{s.dir, filepath.Join(s.dir, "control"), filepath.Join(s.dir, "control", "ca"), filepath.Join(s.dir, "control", "ca", caGeneration), filepath.Join(s.dir, "control", "server"), filepath.Join(s.dir, "control", "server", serverGeneration)} {
		if err := privateDir(dir); err != nil {
			return controlpki.Material{}, err
		}
	}
	var bodies [4][]byte
	for i, rel := range paths {
		path := filepath.Join(s.dir, filepath.FromSlash(rel))
		if err := privateFile(path); err != nil {
			return controlpki.Material{}, err
		}
		bodies[i], err = os.ReadFile(path)
		if err != nil {
			return controlpki.Material{}, err
		}
	}
	return controlpki.Material{CAKey: bodies[0], CACert: bodies[1], ServerKey: bodies[2], ServerCert: bodies[3]}, nil
}

// RemoveJournalOwnedControlIdentity removes only known files in the journal's
// generations. Unknown entries leave the journal in place for inspection.
func (s Store) RemoveJournalOwnedControlIdentity(journal ControlIdentityJournal) error {
	return s.removeJournalOwnedControlIdentity(journal, syncRestoreDirectory)
}

func (s Store) removeJournalOwnedControlIdentity(journal ControlIdentityJournal, syncDir func(string) error) error {
	if _, err := ControlIdentityRelativePaths(journal.CAGeneration, journal.ServerGeneration); err != nil {
		return err
	}
	if err := privateDir(s.dir); err != nil {
		return err
	}
	controlDir := filepath.Join(s.dir, "control")
	if err := privateDir(controlDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, generation := range []struct{ kind, id string }{{"ca", journal.CAGeneration}, {"server", journal.ServerGeneration}} {
		parent := filepath.Join(s.dir, "control", generation.kind)
		if err := privateDir(parent); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		dir := filepath.Join(parent, generation.id)
		info, err := os.Lstat(dir)
		if errors.Is(err, os.ErrNotExist) {
			if err := syncDir(parent); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode().Perm() != 0700 {
			return errors.New("unsafe control identity generation directory")
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			if entry.Name() != "key.pem" && entry.Name() != "cert.pem" {
				return errors.New("unexpected control identity generation entry")
			}
			if err := privateFile(filepath.Join(dir, entry.Name())); err != nil {
				return err
			}
		}
		for _, name := range []string{"key.pem", "cert.pem"} {
			if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if err := syncDir(dir); err != nil {
			return err
		}
		if err := os.Remove(dir); err != nil {
			return err
		}
		if err := syncDir(parent); err != nil {
			return err
		}
	}
	return nil
}

func ensurePrivateDir(path string) error {
	err := os.Mkdir(path, 0700)
	if err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	return privateDir(path)
}

func privateDir(path string) error {
	if err := rejectSymlinkComponents(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode().Perm() != 0700 {
		return fmt.Errorf("control identity directory is not private: %s", filepath.Base(path))
	}
	return nil
}

func privateFile(path string) error {
	if err := rejectSymlinkComponents(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		return fmt.Errorf("control identity file is not private: %s", filepath.Base(path))
	}
	return nil
}

func rejectSymlinkComponents(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	volume := filepath.VolumeName(abs)
	current := volume + string(filepath.Separator)
	for _, part := range strings.Split(strings.TrimPrefix(abs, current), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("control identity path contains a symlink")
		}
	}
	return nil
}
