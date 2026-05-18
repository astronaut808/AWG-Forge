package storage

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

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
	return os.WriteFile(s.StatePath(), b, 0600)
}

func (s Store) WriteRenderedTunnel(tunnel config.Tunnel, serverConf string, clients map[string]string) error {
	if err := os.MkdirAll(s.ClientsDir(tunnel.InterfaceName), 0700); err != nil {
		return err
	}
	if err := os.Chmod(s.dir, 0700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(s.TunnelDir(tunnel.InterfaceName), "server.conf"), []byte(serverConf), 0600); err != nil {
		return err
	}
	for id, conf := range clients {
		if err := os.WriteFile(filepath.Join(s.ClientsDir(tunnel.InterfaceName), id+".conf"), []byte(conf), 0600); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(s.ClientsDir(tunnel.InterfaceName))
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
			if err := os.Remove(filepath.Join(s.ClientsDir(tunnel.InterfaceName), name)); err != nil {
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
	err := os.RemoveAll(s.TunnelDir(interfaceName))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
