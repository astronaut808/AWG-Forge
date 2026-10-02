package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/astronaut808/awg-forge/internal/nodeagent"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"time"
)

func runNode(cfg config.Config, service *app.Service, args []string) error {
	if len(args) == 0 || args[0] != "enroll" {
		return errors.New("usage: awg-forge node enroll --input-file <invitation.json> [--name name] [--timeout duration]")
	}
	flags := flag.NewFlagSet("node enroll", flag.ContinueOnError)
	input := flags.String("input-file", "", "protected enrollment invitation")
	name := flags.String("name", "node", "node display name")
	timeout := flags.Duration("timeout", 10*time.Minute, "enrollment timeout")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || *input == "" || *timeout <= 0 || *timeout > 15*time.Minute {
		return errors.New("usage: awg-forge node enroll --input-file <invitation.json> [--name name] [--timeout duration]")
	}
	invitation, err := readNodeInvitation(*input)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	return nodeagent.Enroll(ctx, service, invitation, *name, func(code string) error {
		_, err := os.Stdout.WriteString("Enrollment verification code: " + code + "\n")
		return err
	})
}

func readNodeInvitation(path string) (controlapi.Invitation, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return controlapi.Invitation{}, errors.New("open enrollment input failed")
	}
	f := os.NewFile(uintptr(fd), path)
	defer func() { _ = f.Close() }()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return controlapi.Invitation{}, errors.New("inspect enrollment input failed")
	}
	if st.Mode&unix.S_IFMT != unix.S_IFREG || st.Mode&0777 != 0600 || int(st.Uid) != os.Geteuid() || st.Nlink != 1 || st.Size < 1 || st.Size > 64<<10 {
		return controlapi.Invitation{}, errors.New("enrollment input must be a private regular file")
	}
	d := json.NewDecoder(io.LimitReader(f, 64<<10+1))
	d.DisallowUnknownFields()
	var invitation controlapi.Invitation
	if err := d.Decode(&invitation); err != nil {
		return controlapi.Invitation{}, errors.New("invalid enrollment input")
	}
	var extra any
	if err := d.Decode(&extra); !errors.Is(err, io.EOF) {
		return controlapi.Invitation{}, errors.New("invalid enrollment input")
	}
	return invitation, nil
}
