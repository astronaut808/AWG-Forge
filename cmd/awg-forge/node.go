package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/astronaut808/awg-forge/internal/nodeagent"
	"golang.org/x/sys/unix"
)

func runNode(cfg config.Config, service *app.Service, args []string) error {
	if len(args) > 0 && (args[0] == "detach" || args[0] == "rebind") {
		return runNodeRecoveryWithAuthority(cfg, service, args, runtime.GOOS, os.Geteuid())
	}
	if len(args) == 0 || args[0] != "enroll" {
		return errors.New("usage: awg-forge node enroll|detach|rebind (see node recovery documentation)")
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

func runNodeRecoveryWithAuthority(_ config.Config, service *app.Service, args []string, operatingSystem string, euid int) (result error) {
	if len(args) == 0 || (args[0] != "detach" && args[0] != "rebind") {
		return errors.New("usage: awg-forge node detach|rebind --confirm-node-id <uuid> --confirm-controller-id <uuid>")
	}
	if operatingSystem != "linux" || euid != 0 {
		return errors.New("node local recovery requires Linux root")
	}
	flags := flag.NewFlagSet("node "+args[0], flag.ContinueOnError)
	nodeID := flags.String("confirm-node-id", "", "current local node identity")
	controllerID := flags.String("confirm-controller-id", "", "current local controller identity")
	var input, name *string
	var timeout *time.Duration
	if args[0] == "rebind" {
		input = flags.String("input-file", "", "protected fresh enrollment invitation")
		name = flags.String("name", "node", "node display name")
		timeout = flags.Duration("timeout", 10*time.Minute, "enrollment timeout")
	}
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || *nodeID == "" || *controllerID == "" {
		return errors.New("node recovery requires explicit current node/controller confirmation")
	}
	if input != nil && (*input == "" || *timeout <= 0 || *timeout > 15*time.Minute) {
		return errors.New("rebind requires protected invitation and bounded timeout")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if timeout != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, *timeout)
		defer cancel()
	}
	r, err := service.BeginNodeRecovery(ctx, *nodeID, *controllerID)
	if err != nil {
		return err
	}
	defer func() { result = errors.Join(result, r.Close()) }()
	if args[0] == "detach" {
		return r.Detach(ctx)
	}
	invitation, err := readNodeInvitation(*input)
	if err != nil {
		return err
	}
	return nodeagent.Enroll(ctx, r, invitation, *name, func(code string) error {
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
