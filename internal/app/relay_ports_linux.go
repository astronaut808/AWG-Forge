//go:build linux

package app

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

func lockRelayNetwork() (func(), error) {
	file, err := os.Open("/proc/self/ns/net")
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return func() { _ = file.Close() }, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			_ = file.Close()
			return nil, fmt.Errorf("lock network namespace: %w", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
