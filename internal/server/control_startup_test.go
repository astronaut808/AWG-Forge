package server

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/observability"
	"github.com/astronaut808/awg-forge/internal/storage"
	"github.com/astronaut808/awg-forge/internal/webtls"
)

func TestControlStartupLockProcess(t *testing.T) {
	dir := os.Getenv("AWG_TEST_CONTROL_LOCK_DIR")
	if dir == "" {
		return
	}
	lock, err := storage.AcquireStateMutationLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = lock.Close() }()
	_, _ = io.WriteString(os.Stdout, "LOCKED\n")
	_, _ = io.Copy(io.Discard, os.Stdin)
}

type startupLogBarrier struct {
	once     sync.Once
	checking chan struct{}
}

func (w *startupLogBarrier) Write(body []byte) (int, error) {
	if strings.Contains(string(body), "control.start.checking") {
		w.once.Do(func() { close(w.checking) })
	}
	return len(body), nil
}

func TestServeContextCancelsExternalStartupLock(t *testing.T) {
	cfg := controllerHTTPTestConfig(t)
	cfg.SessionSecret = "" // Exercise the initialized read path, with no hidden Init lock.
	cfg.WebUIHost = "127.0.0.1"
	cfg.WebUIPort = 0
	log := &startupLogBarrier{checking: make(chan struct{})}
	svc := app.NewWithRuntimeLog(cfg, observability.NewWithWriter("debug", log))
	state, err := svc.Init()
	if err != nil {
		t.Fatal(err)
	}
	// The blocked startup path need only reach the owner lock, not regenerate
	// absent material. A malformed fixture remains fail-closed if allowed to run.
	state.Mode = config.ModeController
	state.Controller = &config.ControllerState{ControllerID: "11111111-1111-4111-8111-111111111111", Control: &config.ControlIdentityState{Enabled: true}}
	if err := storage.New(cfg.ConfigDir).Save(state); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(cfg.ConfigDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	helperCtx, stopHelper := context.WithTimeout(context.Background(), 10*time.Second)
	defer stopHelper()
	// Execute a private copy of this test binary using a fixed command name.
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	helperDir := t.TempDir()
	target, err := os.OpenFile(filepath.Join(helperDir, "control-lock-helper"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		_ = source.Close()
		t.Fatal(err)
	}
	_, copyErr := io.Copy(target, source)
	err = errors.Join(copyErr, target.Close(), source.Close())
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(helperCtx, "./control-lock-helper", "-test.run=^TestControlStartupLockProcess$")
	cmd.Dir = helperDir
	cmd.Env = append(os.Environ(), "AWG_TEST_CONTROL_LOCK_DIR="+cfg.ConfigDir)
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = input.Close()
		if err := cmd.Wait(); err != nil {
			t.Error("external lock helper failed", err)
		}
	}()
	line, err := bufio.NewReader(output).ReadString('\n')
	if err != nil || line != "LOCKED\n" {
		t.Fatal("external lock not held")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- ServeContext(ctx, cfg, svc, webtls.Runtime{}, nil) }()
	select {
	case <-log.checking:
	case <-time.After(2 * time.Second):
		t.Fatal("did not reach StartControl")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("wrong cancellation result", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("startup remained blocked")
	}
	after, err := os.ReadFile(filepath.Join(cfg.ConfigDir, "state.json"))
	if err != nil || string(before) != string(after) {
		t.Fatal("cancelled startup mutated state")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_ = l.Close()
}
