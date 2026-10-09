package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlauth"
)

func TestEnrollmentOnboardingRechecksSessionRecentAuthAndControlState(t *testing.T) {
	f := newControlLifecycleFixture(t)
	ctx := context.Background()
	receipt, err := f.service.PrepareControlEnable(ctx, f.token, func(context.Context, config.State) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.EnableControl(ctx, f.token, receipt, true); err != nil {
		t.Fatal(err)
	}
	invitation, err := f.service.CreateEnrollmentInvitation(ctx, f.token)
	if err != nil {
		t.Fatal(err)
	}
	status, err := f.service.EnrollmentOnboarding(ctx, f.token, invitation.InvitationID)
	if err != nil || status.Status != "waiting" || status.Connected || status.InvitationID != invitation.InvitationID {
		t.Fatalf("waiting onboarding status = %#v, %v", status, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := f.service.EnrollmentOnboarding(cancelled, f.token, invitation.InvitationID); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled request = %v", err)
	}
	f.service.controllerAuthOptions.RecentAuthTTL = time.Nanosecond
	if _, err := f.service.EnrollmentOnboarding(ctx, f.token, invitation.InvitationID); !errors.Is(err, controlauth.ErrRecentAuthRequired) {
		t.Fatalf("stale recent authentication = %v", err)
	}
	f.service.controllerAuthOptions.RecentAuthTTL = 0
	if err := f.service.DisableControl(ctx, f.token); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.EnrollmentOnboarding(ctx, f.token, invitation.InvitationID); err == nil {
		t.Fatal("disabled control accepted onboarding status")
	}
}
