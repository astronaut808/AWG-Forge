package app

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/controlpki"
)

const controlRenewalPoll = time.Minute

var errControlCAMaintenance = errors.New("control CA maintenance required")

// All scheduling derives from the committed certificate, including CA-capped
// leaves. No extra timestamp or generation store participates in authority.
func controlServerRenewalTiming(material controlpki.Material) (due, expiry, caExpiry time.Time, err error) {
	parse := func(body []byte) (*x509.Certificate, error) {
		block, _ := pem.Decode(body)
		if block == nil {
			return nil, errors.New("control certificate unavailable")
		}
		return x509.ParseCertificate(block.Bytes)
	}
	leaf, err := parse(material.ServerCert)
	if err != nil {
		return due, expiry, caExpiry, err
	}
	ca, err := parse(material.CACert)
	if err != nil {
		return due, expiry, caExpiry, err
	}
	return leaf.NotBefore.Add(leaf.NotAfter.Sub(leaf.NotBefore) * 2 / 3), leaf.NotAfter, ca.NotAfter, nil
}

func (s *Service) controlRenewalTime() time.Time {
	if s.controlRenewalNow != nil {
		return s.controlRenewalNow().UTC()
	}
	return time.Now().UTC()
}

// renewControlStartupLocked is reachable only after recovery, while no owner
// exists. Registry and auth must already exist; missing identity is never made.
func (s *Service) renewControlStartupLocked(ctx context.Context, state config.State) error {
	if s.controlOwner != nil || state.Controller == nil || state.Controller.Control == nil || !state.Controller.Control.Enabled {
		return errors.New("startup renewal requires a closed enabled controller")
	}
	control := *state.Controller.Control
	if err := requireControlLoopback(control); err != nil {
		return err
	}
	db, _, err := s.openControlRegistry(ctx)
	if err != nil {
		return err
	}
	if err := db.Close(); err != nil {
		return errors.New("control registry preflight failed")
	}
	material, err := s.store.LoadControlIdentity(control.CAGeneration, control.ServerGeneration)
	if err != nil {
		return errors.New("committed control identity unavailable")
	}
	now := s.controlRenewalTime()
	endpoint := controlpki.Endpoint{BindIP: control.BindIP, Advertised: control.Advertised, Port: control.Port}
	if err := controlpki.ValidateForServerRotation(material, endpoint, control.CAPin, now); err != nil {
		return errors.New("committed control identity cannot renew")
	}
	due, expiry, caExpiry, err := controlServerRenewalTiming(material)
	if err != nil {
		return err
	}
	if now.Before(due) {
		return ctx.Err()
	}
	if !controlRenewalCanProgress(now, expiry, caExpiry) {
		s.log("warn", "control.server.ca_maintenance", "control CA maintenance required; server certificate cannot be extended", nil, nil)
		return ctx.Err() // A still-valid CA-capped leaf may serve until expiry.
	}
	_, err = s.rotateControlServerLeafLocked(ctx, control.ServerGeneration, now, nil, true)
	return err
}

func controlRenewalCanProgress(now, expiry, caExpiry time.Time) bool {
	end := now.Add(controlpki.ServerCertificateTTL)
	if caExpiry.Before(end) {
		end = caExpiry
	}
	return end.Truncate(time.Second).After(expiry)
}

func waitControlRenewal(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (s *Service) runControlServerRenewal(ctx context.Context, owner *controlRuntimeOwner) {
	wait := s.controlRenewalWait
	if wait == nil {
		wait = waitControlRenewal
	}
	backoff := time.Second
	warnedCA := false
	for {
		delay, terminal, err := s.controlServerRenewalAttempt(ctx, owner)
		if ctx.Err() != nil {
			return
		}
		if terminal {
			return
		}
		if errors.Is(err, errControlCAMaintenance) {
			if !warnedCA {
				s.log("warn", "control.server.ca_maintenance", "control CA maintenance required; server certificate cannot be extended", nil, nil)
				warnedCA = true
			}
		} else if err != nil {
			s.log("warn", "control.server.renewal_retry", "control server renewal failed; bounded retry scheduled", nil, nil)
			delay = backoff
			if jitter, err := rand.Int(rand.Reader, big.NewInt(int64(backoff/4)+1)); err == nil {
				delay += time.Duration(jitter.Int64())
			}
			backoff = min(backoff*2, controlRenewalPoll)
		} else {
			backoff = time.Second
		}
		if !wait(ctx, delay) {
			return
		}
	}
}

// Service mutex -> process mutation lock, both cancellable. Every owner and
// generation fence is rechecked after acquiring both locks, before signing.
func (s *Service) controlServerRenewalAttempt(ctx context.Context, owner *controlRuntimeOwner) (delay time.Duration, terminal bool, err error) {
	if err := s.lockControlRequest(ctx); err != nil {
		return 0, false, err
	}
	defer s.unlockStateMutation()
	defer func() {
		if terminal {
			// Close before releasing the generation fence. A same-owner rotation
			// must not publish a successor between this decision and closure.
			owner.runtime.Close()
			s.log("warn", "control.server.renewal_stopped", "control server renewal stopped; explicit inspection or retry is required", nil, nil)
		}
	}()
	if s.controlOwner != owner {
		return 0, true, errors.New("control runtime owner changed")
	}
	if err := ctx.Err(); err != nil {
		return 0, false, err
	}
	if err := s.checkControlMutationJournalsLocked(); err != nil {
		return 0, true, err
	}
	state, err := s.store.Load()
	if err != nil || validateStateMode(state) != nil || state.EffectiveMode() != config.ModeController || state.Controller == nil || state.Controller.ControllerID != owner.controllerID || state.Controller.Control == nil || !state.Controller.Control.Enabled {
		return 0, true, errors.New("control renewal authority changed")
	}
	expected := owner.identity
	expected.Enabled = true // Enable binds before committing Enabled.
	if *state.Controller.Control != expected {
		return 0, true, errStaleControlServerGeneration
	}
	now := s.controlRenewalTime()
	if err := s.recoverControlIdentityLocked(state); err != nil {
		return 0, true, err
	}
	if err := s.recoverControlServerRotationLocked(state, now); err != nil {
		return 0, true, err
	}
	material, err := s.store.LoadControlIdentity(expected.CAGeneration, expected.ServerGeneration)
	if err != nil {
		return 0, true, err
	}
	endpoint := controlpki.Endpoint{BindIP: expected.BindIP, Advertised: expected.Advertised, Port: expected.Port}
	if err := controlpki.Validate(material, endpoint, expected.CAPin, now, false); err != nil {
		return 0, true, err
	}
	due, expiry, caExpiry, err := controlServerRenewalTiming(material)
	if err != nil {
		return 0, true, err
	}
	if now.Before(due) {
		return min(due.Sub(now), controlRenewalPoll), false, nil
	}
	if !controlRenewalCanProgress(now, expiry, caExpiry) {
		return controlRenewalPoll, false, errControlCAMaintenance
	}
	_, err = s.rotateControlServerLeafLocked(ctx, expected.ServerGeneration, now, owner.runtime, false)
	return controlRenewalPoll, false, err
}
