package backup

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/storage"
)

func TestServerNameBackupRestoreValidation(t *testing.T) {
	cfg := testConfig(t)
	service := app.New(cfg)
	if _, err := service.UpdateServerName("BackupName"); err != nil {
		t.Fatal(err)
	}
	archive, err := Create(context.Background(), cfg, service, testPassword, Options{})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := decrypt(archive.Data, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"server_name", "server_name_env"} {
		t.Run(field, func(t *testing.T) {
			var stateJSON []byte
			mutated := mutatePlainZip(t, plain, func(name string, data []byte) []byte {
				if name == "state.json" {
					var state map[string]any
					if err := json.Unmarshal(data, &state); err != nil {
						t.Fatal(err)
					}
					state[field] = "invalid-name"
					stateJSON = mustJSON(t, state)
					return stateJSON
				}
				if name == "metadata.json" {
					var metadata Metadata
					if err := json.Unmarshal(data, &metadata); err != nil {
						t.Fatal(err)
					}
					for i := range metadata.Files {
						if metadata.Files[i].Path == "state.json" {
							metadata.Files[i] = testFileMeta("state.json", stateJSON)
						}
					}
					return mustJSON(t, metadata)
				}
				return data
			})
			encrypted, err := encrypt(mutated, testPassword, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			path := writeTempArchive(t, encrypted)
			if _, err := Verify(context.Background(), cfg, testPassword, path); err == nil || !strings.Contains(err.Error(), "server name") {
				t.Fatalf("verify invalid name = %v", err)
			}
			target := testConfig(t)
			if _, err := app.New(target).Init(); err != nil {
				t.Fatal(err)
			}
			statePath := storage.New(target.ConfigDir).StatePath()
			before, err := os.ReadFile(statePath)
			if err != nil {
				t.Fatal(err)
			}
			if err := Restore(context.Background(), target, testPassword, path); err == nil {
				t.Fatal("restore accepted invalid name")
			}
			after, err := os.ReadFile(statePath)
			if err != nil {
				t.Fatal(err)
			}
			if string(before) != string(after) {
				t.Fatal("invalid backup changed target state")
			}
		})
	}
	target := testConfig(t)
	if err := Restore(context.Background(), target, testPassword, writeTempArchive(t, archive.Data)); err != nil {
		t.Fatal(err)
	}
	state, err := storage.New(target.ConfigDir).Load()
	if err != nil {
		t.Fatal(err)
	}
	if state.ServerName != "BackupName" {
		t.Fatal("valid backup lost server name")
	}
	if _, err := app.New(target).Init(); err != nil {
		t.Fatal(err)
	}
}
