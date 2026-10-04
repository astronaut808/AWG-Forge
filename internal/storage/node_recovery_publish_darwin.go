package storage

import "golang.org/x/sys/unix"

func publishNodeRecoveryJournal(from, to string) error {
	return unix.RenamexNp(from, to, unix.RENAME_EXCL)
}
