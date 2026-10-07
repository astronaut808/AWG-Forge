package storage

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

const NodeRecoveryJournalFileName = ".node-local-recovery.json"

// NodeRecoveryJournal carries hashes of authority-bearing state, never state
// bodies, invitations or keys. The mutation lock protects publication/cleanup.
type NodeRecoveryJournal struct {
	Version            int    `json:"version"`
	OldStateHash       string `json:"old_state_hash"`
	NewStateHash       string `json:"new_state_hash"`
	OldGeneration      string `json:"old_generation"`
	NewGeneration      string `json:"new_generation,omitempty"`
	RenewalGeneration  string `json:"renewal_generation,omitempty"`
	RenewalJournalHash string `json:"renewal_journal_hash,omitempty"`
}

func (s Store) NodeRecoveryJournalPath() string {
	return filepath.Join(s.dir, NodeRecoveryJournalFileName)
}

func validateNodeRecoveryJournal(j NodeRecoveryJournal) error {
	if j.Version != 1 || ValidateControlGeneration(j.OldGeneration) != nil || j.OldStateHash == j.NewStateHash {
		return errors.New("invalid node recovery journal")
	}
	for _, hash := range []string{j.OldStateHash, j.NewStateHash} {
		b, err := hex.DecodeString(hash)
		if err != nil || len(b) != 32 || hex.EncodeToString(b) != hash {
			return errors.New("invalid node recovery journal")
		}
	}
	for _, g := range []string{j.NewGeneration, j.RenewalGeneration} {
		if g != "" && (ValidateControlGeneration(g) != nil || g == j.OldGeneration) {
			return errors.New("invalid node recovery journal")
		}
	}
	if j.NewGeneration != "" && j.NewGeneration == j.RenewalGeneration {
		return errors.New("invalid node recovery journal")
	}
	if (j.RenewalGeneration == "") != (j.RenewalJournalHash == "") {
		return errors.New("invalid node recovery journal")
	}
	if j.RenewalJournalHash != "" {
		b, err := hex.DecodeString(j.RenewalJournalHash)
		if err != nil || len(b) != 32 || hex.EncodeToString(b) != j.RenewalJournalHash {
			return errors.New("invalid node recovery journal")
		}
	}
	return nil
}

func (s Store) CheckNoNodeRecovery() error {
	if _, err := os.Lstat(s.NodeRecoveryJournalPath()); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return errors.New("node local recovery is pending")
}

// CheckNodeRecoveryDirectory rejects path redirection before taking the
// existing offline lease. It never creates an installation for recovery.
func (s Store) CheckNodeRecoveryDirectory() error {
	if err := privateDir(s.dir); err != nil {
		return err
	}
	if err := privateFile(s.StatePath()); err != nil {
		return err
	}
	var st unix.Stat_t
	if err := unix.Lstat(s.dir, &st); err != nil {
		return err
	}
	if int(st.Uid) != os.Geteuid() {
		return errors.New("node recovery directory owner differs")
	}
	if err := unix.Lstat(s.StatePath(), &st); err != nil {
		return err
	}
	if int(st.Uid) != os.Geteuid() || st.Nlink != 1 {
		return errors.New("unsafe node recovery state file")
	}
	for _, name := range []string{StateLockFileName, StateMutationLockFileName} {
		path := filepath.Join(s.dir, name)
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if err := privateFile(path); err != nil {
			return err
		}
		if err := unix.Lstat(path, &st); err != nil {
			return err
		}
		if int(st.Uid) != os.Geteuid() || st.Nlink != 1 {
			return errors.New("unsafe node recovery lock file")
		}
	}
	return nil
}

func (s Store) CheckNodeGenerationAbsent(generation string) error {
	if err := ValidateControlGeneration(generation); err != nil {
		return err
	}
	if err := privateDir(s.dir); err != nil {
		return err
	}
	parent := filepath.Join(s.dir, "node")
	if _, err := os.Lstat(parent); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	if err := privateDir(parent); err != nil {
		return err
	}
	if _, err := os.Lstat(filepath.Join(parent, generation)); !errors.Is(err, os.ErrNotExist) {
		return errors.New("node generation already exists or is unsafe")
	}
	return nil
}

func (s Store) SaveNodeRecoveryJournal(j NodeRecoveryJournal, hook func(string) error) error {
	if err := validateNodeRecoveryJournal(j); err != nil {
		return err
	}
	if err := privateDir(s.dir); err != nil {
		return err
	}
	if err := s.CheckNoNodeRecovery(); err != nil {
		return err
	}
	body, err := json.Marshal(j)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.dir, ".node-local-recovery-*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = f.Close(); _ = os.Remove(f.Name()) }()
	if err := rotationStep(hook, "journal-created"); err != nil {
		return err
	}
	if _, err := f.Write(body); err != nil {
		return err
	}
	if err := rotationStep(hook, "journal-written"); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := rotationStep(hook, "journal-synced"); err != nil {
		return err
	}
	if err := privateDir(s.dir); err != nil {
		return err
	}
	if err := publishNodeRecoveryJournal(f.Name(), s.NodeRecoveryJournalPath()); err != nil {
		return err
	}
	if err := rotationStep(hook, "journal-published"); err != nil {
		return err
	}
	if err := s.SyncStateDirectory(); err != nil {
		return err
	}
	return rotationStep(hook, "journal-durable")
}

func (s Store) LoadNodeRecoveryJournal() (NodeRecoveryJournal, error) {
	if _, err := os.Lstat(s.NodeRecoveryJournalPath()); err != nil {
		return NodeRecoveryJournal{}, err
	}
	b, err := readNodePrivateFile(s.NodeRecoveryJournalPath(), 4096)
	if err != nil {
		return NodeRecoveryJournal{}, err
	}
	d := json.NewDecoder(bytes.NewReader(b))
	// Repeated keys cannot select two different generation/hash authorities.
	tokens := json.NewDecoder(bytes.NewReader(b))
	if start, err := tokens.Token(); err != nil || start != json.Delim('{') {
		return NodeRecoveryJournal{}, errors.New("invalid node recovery journal")
	}
	seen := map[string]bool{}
	for tokens.More() {
		key, err := tokens.Token()
		if err != nil {
			return NodeRecoveryJournal{}, errors.New("invalid node recovery journal")
		}
		name, ok := key.(string)
		if !ok || seen[name] {
			return NodeRecoveryJournal{}, errors.New("ambiguous node recovery journal")
		}
		seen[name] = true
		var value json.RawMessage
		if err := tokens.Decode(&value); err != nil {
			return NodeRecoveryJournal{}, errors.New("invalid node recovery journal")
		}
	}
	d.DisallowUnknownFields()
	var j NodeRecoveryJournal
	if err := d.Decode(&j); err != nil {
		return j, errors.New("invalid node recovery journal")
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return j, errors.New("invalid node recovery journal")
	}
	return j, validateNodeRecoveryJournal(j)
}

func (s Store) DeleteNodeRecoveryJournal() error {
	if _, err := s.LoadNodeRecoveryJournal(); err != nil {
		return err
	}
	if err := os.Remove(s.NodeRecoveryJournalPath()); err != nil {
		return err
	}
	return s.SyncStateDirectory()
}

// RetireNodeGeneration removes only the exact recorded, private generation.
// Unknown entries and linked files preserve evidence rather than broad cleanup.
func (s Store) RetireNodeGeneration(generation string, renewal bool) error {
	if err := ValidateControlGeneration(generation); err != nil {
		return err
	}
	if err := privateDir(s.dir); err != nil {
		return err
	}
	parentName, names := "node", []string{"ca.pem", "cert.pem", "key.pem"}
	if renewal {
		parentName, names = "node-renewal", []string{"key.pem", "csr.der"}
	}
	parent := filepath.Join(s.dir, parentName)
	if _, err := os.Lstat(parent); errors.Is(err, os.ErrNotExist) {
		return s.SyncStateDirectory()
	} else if err != nil {
		return err
	}
	if err := privateDir(parent); err != nil {
		return err
	}
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
	for _, entry := range entries {
		allowed := false
		for _, name := range names {
			allowed = allowed || name == entry.Name()
		}
		if !allowed {
			return errors.New("unexpected node generation entry")
		}
		var st unix.Stat_t
		if err := unix.Lstat(filepath.Join(dir, entry.Name()), &st); err != nil {
			return err
		}
		if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0777 != 0600 || int(st.Uid) != os.Geteuid() || st.Nlink != 1 {
			return errors.New("unsafe node generation entry")
		}
	}
	for _, entry := range entries {
		if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	if err := syncRestoreDirectory(dir); err != nil {
		return err
	}
	if err := os.Remove(dir); err != nil {
		return err
	}
	return syncRestoreDirectory(parent)
}
