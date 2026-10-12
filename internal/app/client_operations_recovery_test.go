package app

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

func TestNodeRecoveryRetiresClientOutcomesOnlyAfterCommit(t *testing.T) {
	for _, detach := range []bool{true, false} {
		t.Run(map[bool]string{true: "detach", false: "rebind"}[detach], func(t *testing.T) {
			s, old := nodeRecoveryFixture(t, time.Hour)
			if err := s.store.SaveClientOperations([]ClientOperationOutcome{}); err != nil {
				t.Fatal(err)
			}
			recovery := recoverySession(t, s, old)
			injected := errors.New("before state commit")
			s.nodeRecoveryStep = func(point string) error {
				if point == "before-state-save" {
					return injected
				}
				return nil
			}
			apply := func() error {
				if detach {
					return recovery.Detach(context.Background())
				}
				inv, approved, key := nodeApprovalFixture(t)
				return recovery.InstallNodeEnrollment(context.Background(), inv, approved, key)
			}
			if !errors.Is(apply(), injected) {
				t.Fatal("expected injected interruption")
			}
			var rows []ClientOperationOutcome
			if err := s.store.LoadClientOperations(&rows); err != nil {
				t.Fatalf("precommit journal retired: %v", err)
			}
			s.nodeRecoveryStep = nil
			if err := s.recoverNodeRecoveryLocked(); err != nil {
				t.Fatal(err)
			}
			if err := apply(); err != nil {
				t.Fatal(err)
			}
			if err := s.store.LoadClientOperations(&rows); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("old authority outcomes survived: %v", err)
			}
		})
	}
}
