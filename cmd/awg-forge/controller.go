package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/sqldb"
	"github.com/astronaut808/awg-forge/internal/storage"
	"golang.org/x/sys/unix"
)

func runController(cfg config.Config, service *app.Service, args []string) error {
	return runControllerWithAuthority(cfg, service, args, runtime.GOOS, os.Geteuid())
}

func runControllerWithAuthority(cfg config.Config, service *app.Service, args []string, operatingSystem string, euid int) error {
	if len(args) != 3 || args[0] != "recover-admin" || args[1] != "--input-file" || args[2] == "" {
		return errors.New("usage: awg-forge controller recover-admin --input-file <root-private-json>")
	}
	if operatingSystem != "linux" || euid != 0 {
		return errors.New("controller administrator recovery requires Linux root")
	}
	lock, err := storage.AcquireStateLock(cfg.ConfigDir)
	if err != nil {
		return fmt.Errorf("controller recovery requires a stopped server: %w", err)
	}
	defer func() { _ = lock.Close() }()
	if err := storage.New(cfg.ConfigDir).CheckRestorePending(); err != nil {
		return err
	}
	state, err := storage.New(cfg.ConfigDir).Load()
	if err != nil {
		return fmt.Errorf("load existing controller state: %w", err)
	}
	if state.EffectiveMode() != config.ModeController || state.Controller == nil {
		return app.ErrInvalidStateMode
	}
	if cfg.DatabaseMode != sqldb.ModeSQLite {
		return app.ErrControllerActivationRequiresDB
	}
	info, err := os.Lstat(cfg.DatabasePath)
	if err != nil {
		return fmt.Errorf("existing controller database required: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("controller database is not a regular file")
	}
	auth, db, err := loadControllerAuth(cfg, state)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	input, err := readRootRecoveryInput(args[2])
	if err != nil {
		return err
	}
	timeout := cfg.DatabaseQueryTimeout
	if timeout < 30*time.Second {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	result, err := service.RecoverControllerAdmin(ctx, auth, input.Username, input.Password)
	if err != nil {
		return fmt.Errorf("recover controller administrator: %w", err)
	}
	// These values are displayed once and never sent to audit or runtime logs.
	if _, err := fmt.Fprintf(os.Stdout, "TOTP secret: %s\nRecovery codes:\n%s\n", result.TOTPSecret, strings.Join(result.RecoveryCodes, "\n")); err != nil {
		return fmt.Errorf("display recovery material: %w", err)
	}
	return nil
}

type rootRecoveryInput struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

func readRootRecoveryInput(path string) (rootRecoveryInput, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return rootRecoveryInput{}, fmt.Errorf("open root-private recovery input: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	defer func() { _ = file.Close() }()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return rootRecoveryInput{}, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0777 != 0600 || stat.Uid != 0 || stat.Nlink != 1 || stat.Size < 1 || stat.Size > 2048 {
		return rootRecoveryInput{}, errors.New("recovery input must be a root-owned 0600 regular file")
	}
	decoder := json.NewDecoder(io.LimitReader(file, 2049))
	decoder.DisallowUnknownFields()
	var input rootRecoveryInput
	if err := decoder.Decode(&input); err != nil {
		return rootRecoveryInput{}, errors.New("invalid recovery input")
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return rootRecoveryInput{}, errors.New("invalid recovery input")
	}
	if input.Username == "" || input.Password == "" {
		return rootRecoveryInput{}, errors.New("recovery input requires username and password")
	}
	return input, nil
}
