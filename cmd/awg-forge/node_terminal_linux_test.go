//go:build linux

package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestNodeSecretTerminalHidesInputAndRestoresEcho(t *testing.T) {
	for _, scenario := range []string{"valid", "invalid", "cancel"} {
		t.Run(scenario, func(t *testing.T) {
			master, err := unix.Open("/dev/ptmx", unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = unix.Close(master) }()
			if err := unix.IoctlSetPointerInt(master, unix.TIOCSPTLCK, 0); err != nil {
				t.Fatal(err)
			}
			number, err := unix.IoctlGetInt(master, unix.TIOCGPTN)
			if err != nil {
				t.Fatal(err)
			}
			slave, err := unix.Open(fmt.Sprintf("/dev/pts/%d", number), unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = unix.Close(slave) }()
			original, err := nodeGetTermios(slave)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			type result struct {
				secret string
				err    error
			}
			done := make(chan result, 1)
			go func() { value, err := readNodeSecret(ctx, slave); done <- result{value, err} }()
			for {
				term, err := nodeGetTermios(slave)
				if err != nil {
					t.Fatal(err)
				}
				if term.Lflag&(unix.ECHO|unix.ECHONL) == 0 {
					break
				}
				if ctx.Err() != nil {
					t.Fatal("terminal did not hide input")
				}
				time.Sleep(time.Millisecond)
			}
			secret := base64.RawURLEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
			if scenario == "cancel" {
				cancel()
			} else {
				input := secret + "\n"
				if scenario == "invalid" {
					input = "invalid\n"
				}
				if _, err := unix.Write(master, []byte(input)); err != nil {
					t.Fatal(err)
				}
			}
			var got result
			select {
			case got = <-done:
			case <-time.After(4 * time.Second):
				t.Fatal("terminal cancellation timed out")
			}
			if scenario == "valid" && (got.err != nil || got.secret != secret) {
				t.Fatal("private terminal input failed")
			}
			if scenario != "valid" && got.err == nil {
				t.Fatal("invalid/cancelled terminal input accepted")
			}
			term, err := nodeGetTermios(slave)
			if err != nil || term.Lflag != original.Lflag {
				t.Fatal("terminal echo flags were not restored")
			}
			var output [256]byte
			n, err := unix.Read(master, output[:])
			if n > 0 || (err != nil && !errors.Is(err, unix.EAGAIN)) {
				t.Fatal("terminal echoed secret input")
			}
		})
	}
}
