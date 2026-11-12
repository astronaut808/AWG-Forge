package storage

import "golang.org/x/sys/unix"

// Atomic no-replace rename leaves one name/link even if the process dies
// immediately after publication. Unsupported filesystems fail closed.
func publishNodeRecoveryJournal(from, to string) error {
	return unix.Renameat2(unix.AT_FDCWD, from, unix.AT_FDCWD, to, unix.RENAME_NOREPLACE)
}
