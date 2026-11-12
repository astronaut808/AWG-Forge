package storage

import (
	"os"
	"path/filepath"
	"testing"
)

func TestClientOperationsStorageRejectsUnsafeEvidence(t *testing.T) {
	for _, mode := range []string{"permissions", "symlink", "oversize", "null", "unknown", "trailing"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			p := filepath.Join(root, ".client-operations.json")
			raw := []byte(`[{"id":"safe"}]`)
			switch mode {
			case "oversize":
				raw = make([]byte, (2<<20)+1)
			case "null":
				raw = []byte(`null`)
			case "unknown":
				raw = []byte(`[{"secret":"must reject"}]`)
			case "trailing":
				raw = []byte(`[] []`)
			}
			if err := os.WriteFile(p, raw, 0600); err != nil {
				t.Fatal(err)
			}
			if mode == "permissions" {
				if err := os.Chmod(p, 0644); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "symlink" {
				if err := os.Rename(p, p+"-target"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(p+"-target", p); err != nil {
					t.Fatal(err)
				}
			}
			var rows []struct {
				ID string `json:"id"`
			}
			if err := New(root).LoadClientOperations(&rows); err == nil {
				t.Fatal("unsafe evidence accepted")
			}
		})
	}
}
