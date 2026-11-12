package storage

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"path/filepath"
)

const NodeIdentityJournalFileName = ".node-identity-installation"

type NodeIdentityMaterial struct{ CACert, Certificate, PrivateKey []byte }
type NodeIdentityJournal struct {
	Generation string `json:"generation"`
}

func NodeIdentityRelativePaths(generation string) ([]string, error) {
	if err := ValidateControlGeneration(generation); err != nil {
		return nil, err
	}
	return []string{
		filepath.ToSlash(filepath.Join("node", generation, "ca.pem")),
		filepath.ToSlash(filepath.Join("node", generation, "cert.pem")),
		filepath.ToSlash(filepath.Join("node", generation, "key.pem")),
	}, nil
}

func (s Store) NodeIdentityJournalPath() string {
	return filepath.Join(s.dir, NodeIdentityJournalFileName)
}
func (s Store) SaveNodeIdentityJournal(j NodeIdentityJournal) error {
	if _, err := NodeIdentityRelativePaths(j.Generation); err != nil {
		return err
	}
	b, err := json.Marshal(j)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(s.NodeIdentityJournalPath(), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, we := f.Write(b)
	se := f.Sync()
	ce := f.Close()
	if err := errors.Join(we, se, ce); err != nil {
		return err
	}
	return syncRestoreDirectory(s.dir)
}
func (s Store) LoadNodeIdentityJournal() (NodeIdentityJournal, error) {
	if _, err := os.Lstat(s.NodeIdentityJournalPath()); err != nil {
		return NodeIdentityJournal{}, err
	}
	b, err := readNodePrivateFile(s.NodeIdentityJournalPath(), 4096)
	if err != nil {
		return NodeIdentityJournal{}, err
	}
	var j NodeIdentityJournal
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&j); err != nil {
		return NodeIdentityJournal{}, errors.New("invalid node identity journal")
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return NodeIdentityJournal{}, errors.New("invalid node identity journal")
	}
	if _, err := NodeIdentityRelativePaths(j.Generation); err != nil {
		return NodeIdentityJournal{}, errors.New("invalid node identity journal")
	}
	return j, nil
}
func (s Store) DeleteNodeIdentityJournal() error {
	err := os.Remove(s.NodeIdentityJournalPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (s Store) SaveNodeIdentity(generation string, material NodeIdentityMaterial) error {
	paths, err := NodeIdentityRelativePaths(generation)
	if err != nil {
		return err
	}
	if len(material.CACert) == 0 || len(material.Certificate) == 0 || len(material.PrivateKey) == 0 {
		return errors.New("invalid node identity")
	}
	if err := ensurePrivateDir(s.dir); err != nil {
		return err
	}
	base := filepath.Join(s.dir, "node")
	if err := ensurePrivateDir(base); err != nil {
		return err
	}
	dir := filepath.Join(base, generation)
	if _, err := os.Lstat(dir); err == nil {
		return errors.New("node identity generation already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Mkdir(dir, 0700); err != nil {
		return err
	}
	for i, body := range [][]byte{material.CACert, material.Certificate, material.PrivateKey} {
		path := filepath.Join(s.dir, filepath.FromSlash(paths[i]))
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		_, we := f.Write(body)
		se := f.Sync()
		ce := f.Close()
		if err := errors.Join(we, se, ce); err != nil {
			return err
		}
	}
	if err := syncRestoreDirectory(dir); err != nil {
		return err
	}
	if err := syncRestoreDirectory(base); err != nil {
		return err
	}
	return syncRestoreDirectory(s.dir)
}

func (s Store) LoadNodeIdentity(generation string) (NodeIdentityMaterial, error) {
	paths, err := NodeIdentityRelativePaths(generation)
	if err != nil {
		return NodeIdentityMaterial{}, err
	}
	for _, dir := range []string{s.dir, filepath.Join(s.dir, "node"), filepath.Join(s.dir, "node", generation)} {
		if err := privateDir(dir); err != nil {
			return NodeIdentityMaterial{}, err
		}
	}
	var bodies [3][]byte
	for i, rel := range paths {
		path := filepath.Join(s.dir, filepath.FromSlash(rel))
		bodies[i], err = readNodePrivateFile(path, 1<<20)
		if err != nil {
			return NodeIdentityMaterial{}, err
		}
	}
	return NodeIdentityMaterial{CACert: bodies[0], Certificate: bodies[1], PrivateKey: bodies[2]}, nil
}

func readNodePrivateFile(path string, limit int64) ([]byte, error) {
	if err := rejectSymlinkComponents(path); err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), path)
	defer func() { _ = f.Close() }()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return nil, err
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0777 != 0600 || int(st.Uid) != os.Geteuid() || st.Nlink != 1 || st.Size < 1 || st.Size > limit {
		return nil, fmt.Errorf("invalid node identity file")
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, fmt.Errorf("invalid node identity file")
	}
	return b, nil
}
