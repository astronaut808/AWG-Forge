//go:build !linux && !darwin

package storage

import "errors"

func publishNodeRecoveryJournal(_, _ string) error {
	return errors.New("node recovery publication unsupported on this platform")
}
