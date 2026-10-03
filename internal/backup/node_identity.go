package backup

import (
	"errors"
	"strings"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/storage"
)

// validateNodeArchive admits only the committed immutable generation. Public
// metadata alone must never make an incomplete credential archive restorable.
func validateNodeArchive(files []restoreFile, state config.State) error {
	var paths []string
	if state.NodeConnection != nil {
		if state.EffectiveMode() != config.ModeNode || state.ManagedNode == nil || app.ValidateNodeConnection(state.NodeConnection) != nil {
			return errors.New("backup node connection is invalid")
		}
		var err error
		paths, err = storage.NodeIdentityRelativePaths(state.NodeConnection.CredentialGeneration)
		if err != nil {
			return errors.New("backup node generation is invalid")
		}
	}
	bodies := make(map[string][]byte, len(paths))
	for _, file := range files {
		if file.Path == storage.NodeRecoveryJournalFileName || strings.HasPrefix(file.Path, storage.NodeRecoveryJournalFileName+"/") {
			return errors.New("backup contains node recovery evidence")
		}
		if !strings.HasPrefix(file.Path, "node/") {
			continue
		}
		found := false
		for _, path := range paths {
			if file.Path == path {
				found = true
				break
			}
		}
		if !found || bodies[file.Path] != nil {
			return errors.New("backup has unexpected node credentials")
		}
		bodies[file.Path] = file.Data
	}
	if len(paths) == 0 {
		return nil
	}
	if len(bodies) != len(paths) {
		return errors.New("backup node credentials are incomplete")
	}
	material := storage.NodeIdentityMaterial{CACert: bodies[paths[0]], Certificate: bodies[paths[1]], PrivateKey: bodies[paths[2]]}
	if err := app.ValidateNodeIdentity(state.NodeConnection, material, true); err != nil {
		return errors.New("backup node credentials are invalid")
	}
	return nil
}
