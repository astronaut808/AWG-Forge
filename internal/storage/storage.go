package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
)

type Store struct {
	dir string
}

func New(dir string) Store {
	return Store{dir: dir}
}

func (s Store) StatePath() string { return filepath.Join(s.dir, "state.json") }
func (s Store) TunnelDir(tunnel string) string {
	return filepath.Join(s.dir, "tunnels", tunnel)
}
func (s Store) ClientsDir(tunnel string) string {
	return filepath.Join(s.TunnelDir(tunnel), "clients")
}

func (s Store) safeTunnelDir(tunnel string) (string, error) {
	tunnel, err := safePathComponent("tunnel", tunnel)
	if err != nil {
		return "", err
	}
	return filepath.Join(s.dir, "tunnels", tunnel), nil
}

func (s Store) safeClientsDir(tunnel string) (string, error) {
	dir, err := s.safeTunnelDir(tunnel)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "clients"), nil
}

func (s Store) Load() (config.State, error) {
	b, err := os.ReadFile(s.StatePath())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return config.State{}, os.ErrNotExist
		}
		return config.State{}, err
	}
	var state config.State
	if err := json.Unmarshal(b, &state); err != nil {
		return config.State{}, err
	}
	return state, nil
}

func (s Store) Save(state config.State) error {
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(s.dir, 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.dir, ".state-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	removeTmp := true
	defer func() {
		if removeTmp {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, s.StatePath()); err != nil {
		return err
	}
	removeTmp = false
	if dir, err := os.Open(s.dir); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

func (s Store) BackupState(state config.State, reason string) (string, error) {
	dir := filepath.Join(s.dir, "backups")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return "", err
	}
	reason = sanitizeBackupReason(reason)
	name := "state-" + time.Now().UTC().Format("20060102-150405") + "-" + reason + ".json"
	path := filepath.Join(dir, name)
	return path, os.WriteFile(path, b, 0600)
}

func sanitizeBackupReason(reason string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(reason) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		case r == ' ':
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-_")
	if out == "" {
		return "backup"
	}
	return out
}

func (s Store) WriteRenderedTunnel(tunnel config.Tunnel, serverConf string, clients map[string]string) error {
	tunnelDir, err := s.safeTunnelDir(tunnel.InterfaceName)
	if err != nil {
		return err
	}
	clientsDir, err := s.safeClientsDir(tunnel.InterfaceName)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(clientsDir, 0700); err != nil {
		return err
	}
	if err := os.Chmod(s.dir, 0700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(tunnelDir, "server.conf"), []byte(serverConf), 0600); err != nil {
		return err
	}
	for id, conf := range clients {
		id, err := safePathComponent("client", id)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(clientsDir, id+".conf"), []byte(conf), 0600); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(clientsDir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || filepath.Ext(name) != ".conf" {
			continue
		}
		id := name[:len(name)-len(".conf")]
		if _, ok := clients[id]; !ok {
			if _, err := safePathComponent("client", id); err != nil {
				return err
			}
			if err := os.Remove(filepath.Join(clientsDir, name)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s Store) DeleteRenderedTunnel(interfaceName string) error {
	if interfaceName == "" {
		return nil
	}
	tunnelDir, err := s.safeTunnelDir(interfaceName)
	if err != nil {
		return err
	}
	err = os.RemoveAll(tunnelDir)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func safePathComponent(label, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s path component is empty", label)
	}
	if value == "." || strings.Contains(value, "..") || strings.ContainsAny(value, `/\`+"\x00") {
		return "", fmt.Errorf("invalid %s path component", label)
	}
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '.', r == '_', r == '-':
		default:
			return "", fmt.Errorf("invalid %s path component", label)
		}
	}
	return value, nil
}
