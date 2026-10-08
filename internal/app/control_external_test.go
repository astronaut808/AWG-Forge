package app

import (
	"context"
	"errors"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/astronaut808/awg-forge/internal/config"
)

func TestControlExternalRequiresFreshExplicitConsent(t *testing.T) {
	f := newControlLifecycleFixtureEndpoint(t, "127.0.0.1", "control.example.test")
	ctx := context.Background()
	before, err := f.service.State()
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.StartControl(ctx); err != nil {
		t.Fatal(err)
	}
	assertLoopbackFree(t, f.address)
	receipt := f.receipt(t)
	// A DNS SAN is external even with a loopback bind; the old entry must deny it.
	if err := f.service.EnableControl(ctx, f.token, receipt, true); err == nil {
		t.Fatal("external endpoint enabled without consent")
	}
	if err := f.service.EnableControlWithOptions(ctx, f.token, receipt, ControlEnableOptions{BackupRetained: true, AllowNonLoopback: true}); err == nil {
		t.Fatal("denied receipt was reused")
	}
	assertLoopbackFree(t, f.address)
	after, err := f.service.State()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("denial changed state")
	}
	receipt = f.receipt(t)
	if err := f.service.EnableControlWithOptions(ctx, "forged", receipt, ControlEnableOptions{BackupRetained: true, AllowNonLoopback: true}); err == nil {
		t.Fatal("consent bypassed authentication")
	}
	assertLoopbackFree(t, f.address)
	if err := f.service.enableControl(ctx, f.token, f.receipt(t), lifecycleRoutes(f.service), true); err != nil {
		t.Fatal(err)
	}
	waitServerRotationTLS(t, f.client, f.url)
	if err := f.service.StartControl(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := newFastControllerTestService(f.cfg).rotateControlServerLeaf(ctx, f.control.ServerGeneration, time.Now(), nil); err == nil {
		t.Fatal("external owner fencing bypassed")
	}
	if err := f.service.ShutdownControl(); err != nil {
		t.Fatal(err)
	}
	assertLoopbackFree(t, f.address)
	restarted := newFastControllerTestService(f.cfg)
	t.Cleanup(func() { _ = restarted.ShutdownControl() })
	if _, err := restarted.Init(); err != nil {
		t.Fatal(err)
	}
	assertLoopbackFree(t, f.address)
	if err := restarted.restartControlLoopback(ctx, lifecycleRoutes(restarted)); err != nil {
		t.Fatal(err)
	}
	waitServerRotationTLS(t, f.client, f.url)
	if err := restarted.DisableControl(ctx, f.token); err != nil {
		t.Fatal(err)
	}
	assertLoopbackFree(t, f.address)
	if err := restarted.StartControl(ctx); err != nil {
		t.Fatal(err)
	}
	assertLoopbackFree(t, f.address)
	after, err = restarted.State()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("external lifecycle changed tunnel/configuration state")
	}
}

func TestControlExternalBindAndCommitFailures(t *testing.T) {
	for _, point := range []string{"bind", "after-bind", "before-state-save", "after-state-save", "after-state-sync"} {
		t.Run(point, func(t *testing.T) {
			f := newControlLifecycleFixtureEndpoint(t, "127.0.0.1", "control.example.test")
			ctx := context.Background()
			before, err := f.service.State()
			if err != nil {
				t.Fatal(err)
			}
			receipt := f.receipt(t)
			if point == "bind" {
				listener, err := net.Listen("tcp", f.address)
				if err != nil {
					t.Fatal(err)
				}
				defer func() { _ = listener.Close() }()
			} else {
				f.service.controlRuntimeStep = func(current string) error {
					if current == point {
						return errors.New("injected lifecycle failure")
					}
					return nil
				}
			}
			if err := f.service.EnableControlWithOptions(ctx, f.token, receipt, ControlEnableOptions{BackupRetained: true, AllowNonLoopback: true}); err == nil {
				t.Fatal("injected failure accepted")
			}
			if err := f.service.EnableControlWithOptions(ctx, f.token, receipt, ControlEnableOptions{BackupRetained: true, AllowNonLoopback: true}); err == nil {
				t.Fatal("failed receipt reused")
			}
			if point != "bind" {
				assertLoopbackFree(t, f.address)
			}
			after, err := f.service.State()
			if err != nil {
				t.Fatal(err)
			}
			expectedEnabled := point == "after-state-save" || point == "after-state-sync"
			if after.Controller.Control.Enabled != expectedEnabled {
				t.Fatal("uncertain commit was rolled back or published early")
			}
			after.Controller.Control.Enabled = false
			if !reflect.DeepEqual(before, after) {
				t.Fatal("failure changed local state")
			}
		})
	}
}

func TestControlExternalPrepareKeepsEndpointValidation(t *testing.T) {
	for _, endpoint := range []ControlIdentityRequest{
		{BindIP: "0.0.0.0", Advertised: "control.example.test", Port: 8443},
		{BindIP: "::", Advertised: "control.example.test", Port: 8443},
		{BindIP: "224.0.0.1", Advertised: "control.example.test", Port: 8443},
		{BindIP: "::ffff:127.0.0.1", Advertised: "control.example.test", Port: 8443},
		{BindIP: "fe80::1%eth0", Advertised: "control.example.test", Port: 8443},
		{BindIP: "control.example.test", Advertised: "control.example.test", Port: 8443},
		{BindIP: "127.0.0.1", Advertised: "*.example.test", Port: 8443},
		{BindIP: "127.0.0.1", Advertised: "control.example.test", Port: 80},
	} {
		t.Run(endpoint.BindIP+"/"+endpoint.Advertised, func(t *testing.T) {
			cfg := controllerTestConfig(t)
			service := newFastControllerTestService(cfg)
			if _, err := service.Init(); err != nil {
				t.Fatal(err)
			}
			activation, err := service.ActivateController(context.Background(), controllerTestActivationRequest(t, time.Now()))
			if err != nil {
				t.Fatal(err)
			}
			before, err := service.State()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.PrepareAuthenticatedControl(context.Background(), activation.Authentication.Token, endpoint); err == nil {
				t.Fatal("unsafe endpoint prepared")
			}
			after, err := service.State()
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("invalid preparation changed state")
			}
		})
	}
	// Preparation is authenticated and disabled even for a specific external IP.
	cfg := controllerTestConfig(t)
	service := newFastControllerTestService(cfg)
	activation, err := service.ActivateController(context.Background(), controllerTestActivationRequest(t, time.Now()))
	if err != nil {
		t.Fatal(err)
	}
	request := ControlIdentityRequest{BindIP: "192.0.2.10", Advertised: "control.example.test", Port: 8443}
	if _, err := service.PrepareAuthenticatedControl(context.Background(), "forged", request); err == nil {
		t.Fatal("preparation bypassed authentication")
	}
	prepared, err := service.PrepareAuthenticatedControl(context.Background(), activation.Authentication.Token, request)
	if err != nil || prepared.Enabled {
		t.Fatalf("external preparation: enabled=%v, error=%v", prepared.Enabled, err)
	}
	receipt, err := service.PrepareControlEnable(context.Background(), activation.Authentication.Token, func(_ context.Context, state config.State) error {
		if state.Controller.Control.Enabled || *state.Controller.Control != prepared {
			return errors.New("backup did not capture exact disabled endpoint")
		}
		return nil
	})
	if err != nil || receipt == nil {
		t.Fatal("external backup preparation failed")
	}
}
