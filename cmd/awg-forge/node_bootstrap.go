package main

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"os"
	"regexp"

	"github.com/astronaut808/awg-forge/internal/buildinfo"
	"github.com/astronaut808/awg-forge/internal/controlapi"
	"github.com/astronaut808/awg-forge/internal/nodeagent"
	"github.com/google/uuid"
	"golang.org/x/sys/unix"
)

type nodeInvitationInput struct {
	file, endpoint, pin, id string
	secretFD                int
}

func (i *nodeInvitationInput) flags(f *flag.FlagSet) {
	f.StringVar(&i.file, "input-file", "", "protected invitation (existing interface)")
	f.StringVar(&i.endpoint, "controller-url", "", "exact HTTPS controller endpoint")
	f.StringVar(&i.pin, "ca-pin", "", "controller CA SPKI SHA256 pin")
	f.StringVar(&i.id, "invitation-id", "", "public one-time invitation UUID")
	f.IntVar(&i.secretFD, "secret-fd", -1, "private descriptor for secret; default is hidden TTY")
}

func (i nodeInvitationInput) validate(name string) error {
	if !nodeagent.ValidNodeName(name) {
		return errors.New("invalid node name")
	}
	if i.file != "" {
		if i.endpoint != "" || i.pin != "" || i.id != "" || i.secretFD != -1 {
			return errors.New("choose protected file or public bootstrap parameters")
		}
		return nil
	}
	if i.secretFD < -1 || i.secretFD > 1024 {
		return errors.New("invalid secret descriptor")
	}
	return nodeagent.ValidateBootstrapParameters(i.endpoint, i.pin, i.id, name)
}

func (i nodeInvitationInput) read(ctx context.Context) (controlapi.Invitation, error) {
	if i.file != "" {
		return readNodeInvitation(i.file)
	}
	invitation, err := nodeagent.BootstrapInvitation(ctx, i.endpoint, i.pin, i.id)
	if err != nil {
		return controlapi.Invitation{}, err
	}
	invitation.Secret, err = readNodeSecret(ctx, i.secretFD)
	return invitation, err
}

// installer-check proves feature compatibility using linked metadata, never
// operator-overridable AWG_FORGE_VERSION/COMMIT environment values.
func runNodeInstallerCheck(args []string) error {
	f := flag.NewFlagSet("node installer-check", flag.ContinueOnError)
	version := f.String("artifact-version", "", "expected compiled version")
	commit := f.String("artifact-commit", "", "expected compiled commit")
	endpoint := f.String("controller-url", "", "public controller endpoint")
	pin := f.String("ca-pin", "", "public CA SPKI pin")
	id := f.String("invitation-id", "", "public invitation UUID")
	name := f.String("name", "node", "node name")
	nodeID := f.String("confirm-node-id", "", "old node identity for explicit rebind")
	controllerID := f.String("confirm-controller-id", "", "old controller identity for explicit rebind")
	if err := f.Parse(args); err != nil {
		return err
	}
	if f.NArg() != 0 || !regexp.MustCompile(`^(v[0-9]+\.[0-9]+\.[0-9]+|local-[a-f0-9]{12})$`).MatchString(*version) || !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(*commit) || *version != buildinfo.Version || *commit != buildinfo.Commit {
		return errors.New("unsupported or mismatched installer artifact")
	}
	if *endpoint != "" || *pin != "" || *id != "" {
		if err := nodeagent.ValidateBootstrapParameters(*endpoint, *pin, *id, *name); err != nil {
			return err
		}
	}
	if *nodeID != "" || *controllerID != "" {
		for _, value := range []string{*nodeID, *controllerID} {
			parsed, err := uuid.Parse(value)
			if err != nil || parsed.String() != value {
				return errors.New("canonical old node/controller UUIDs required")
			}
		}
	}
	_, err := os.Stdout.WriteString("installer-onboarding-v1 compatible\n")
	return err
}

// readNodeSecret reads one bounded token from a private FD or hidden terminal.
// Polling makes cancellation restore terminal echo before the process exits.
func readNodeSecret(ctx context.Context, descriptor int) (string, error) {
	fd := -1
	var err error
	if descriptor == -1 {
		fd, err = unix.Open("/dev/tty", unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	} else {
		fd, err = unix.Dup(descriptor)
	}
	if err != nil {
		return "", errors.New("secret requires a TTY or explicit private FD")
	}
	defer func() { _ = unix.Close(fd) }()
	var st unix.Stat_t
	if unix.Fstat(fd, &st) != nil {
		return "", errors.New("inspect secret descriptor failed")
	}
	mode := st.Mode & unix.S_IFMT
	if mode == unix.S_IFREG {
		if st.Mode&0777 != 0600 || int(st.Uid) != os.Geteuid() || st.Nlink != 1 || st.Size < 43 || st.Size > 44 {
			return "", errors.New("secret file descriptor must be private")
		}
	} else if mode != unix.S_IFIFO && mode != unix.S_IFCHR {
		return "", errors.New("unsupported secret descriptor")
	}
	term, termErr := nodeGetTermios(fd)
	if termErr == nil {
		hidden := *term
		hidden.Lflag &^= unix.ECHO | unix.ECHONL
		if nodeSetTermios(fd, &hidden) != nil {
			return "", errors.New("cannot hide secret input")
		}
		defer func() { _ = nodeSetTermios(fd, term) }()
		_, _ = os.Stderr.WriteString("Invitation secret (hidden): ")
		defer func() { _, _ = os.Stderr.WriteString("\n") }()
	} else if descriptor == -1 || mode == unix.S_IFCHR {
		return "", errors.New("secret requires a TTY or explicit private FD")
	}
	value := make([]byte, 0, 44)
	defer func() { clear(value) }()
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		if _, err := unix.Poll(fds, 200); err != nil && !errors.Is(err, unix.EINTR) {
			return "", errors.New("read secret descriptor failed")
		}
		if fds[0].Revents == 0 {
			continue
		}
		var one [1]byte
		n, err := unix.Read(fd, one[:])
		if err != nil {
			return "", errors.New("read secret descriptor failed")
		}
		if n == 0 || one[0] == '\n' {
			break
		}
		if len(value) >= 43 {
			return "", errors.New("invalid invitation secret")
		}
		value = append(value, one[0])
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(string(value))
	clear(decoded)
	if err != nil || len(decoded) != 32 {
		return "", errors.New("invalid invitation secret")
	}
	return string(value), nil
}
