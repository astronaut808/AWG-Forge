package app

import (
	"context"
	"testing"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/storage"
)

func TestFirstEnrollmentExclusiveServeLease(t *testing.T) {
	ctx := context.Background()
	cfg := config.Config{ConfigDir: t.TempDir()}
	s := New(cfg)
	owner, err := storage.AcquireStateLock(cfg.ConfigDir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.BeginNodeEnrollment(ctx); err == nil {
		t.Fatal("live serve owner bypassed")
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	session, err := s.BeginNodeEnrollment(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := storage.AcquireStateLock(cfg.ConfigDir); err == nil {
		t.Fatal("enrollment lease did not exclude serve")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := session.PreflightNodeEnrollment(ctx); err == nil {
		t.Fatal("closed enrollment session reused")
	}
	owner, err = storage.AcquireStateLock(cfg.ConfigDir)
	if err != nil {
		t.Fatal(err)
	}
	_ = owner.Close()
}
