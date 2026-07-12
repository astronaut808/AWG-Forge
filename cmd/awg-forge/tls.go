package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/audit"
	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/webtls"
)

func runTLS(cfg config.Config, svc *app.Service, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: awg-forge tls status | tls use manual --cert <path> --key <path> [--server-name name] | tls use environment | tls disable")
	}
	switch args[0] {
	case "status":
		if len(args) != 1 {
			return errors.New("usage: awg-forge tls status")
		}
		runtime, err := webtls.Load(cfg)
		if err != nil {
			return err
		}
		printTLSStatus(cfg, runtime)
		return nil
	case "use":
		return runTLSUse(cfg, svc, args[1:])
	case "disable":
		if len(args) != 1 {
			return errors.New("usage: awg-forge tls disable")
		}
		if err := webtls.Save(cfg, webtls.Settings{Mode: webtls.ModeOff}); err != nil {
			return err
		}
		svc.Audit().Log(context.Background(), audit.Event{Level: "info", Event: "tls.disabled", Message: "TLS disabled", Fields: map[string]any{"mode": "off"}})
		fmt.Println("OK   TLS disabled; restart awg-forge to apply it")
		return nil
	default:
		return errors.New("usage: awg-forge tls status | tls use manual --cert <path> --key <path> [--server-name name] | tls use environment | tls disable")
	}
}

func runTLSUse(cfg config.Config, svc *app.Service, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: awg-forge tls use manual --cert <path> --key <path> [--server-name name] | tls use environment")
	}
	if args[0] == "environment" {
		if len(args) != 1 {
			return errors.New("usage: awg-forge tls use environment")
		}
		runtime, err := webtls.UseEnvironment(cfg)
		if err != nil {
			return err
		}
		svc.Audit().Log(context.Background(), audit.Event{Level: "info", Event: "tls.environment.selected", Message: "TLS environment configuration selected", Fields: map[string]any{"mode": runtime.Settings.Mode}})
		fmt.Println("OK   TLS environment configuration selected; restart awg-forge to apply it")
		return nil
	}
	if args[0] != "manual" {
		return errors.New("usage: awg-forge tls use manual --cert <path> --key <path> [--server-name name] | tls use environment")
	}
	flags := flag.NewFlagSet("tls use manual", flag.ContinueOnError)
	certFile := flags.String("cert", "", "manual TLS certificate PEM path")
	keyFile := flags.String("key", "", "manual TLS private key PEM path")
	serverName := flags.String("server-name", "", "optional DNS name or IP expected in the certificate SAN")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: awg-forge tls use manual --cert <path> --key <path> [--server-name name]")
	}
	settings := webtls.Settings{Mode: webtls.ModeManual, CertFile: *certFile, KeyFile: *keyFile, ServerName: *serverName}
	if err := webtls.Save(cfg, settings); err != nil {
		return err
	}
	svc.Audit().Log(context.Background(), audit.Event{Level: "info", Event: "tls.manual.configured", Message: "manual TLS configuration saved", Fields: map[string]any{"mode": "manual"}})
	fmt.Println("OK   TLS manual configuration saved; restart awg-forge to apply it")
	return nil
}

func printTLSStatus(cfg config.Config, runtime webtls.Runtime) {
	status := runtime.Status
	fmt.Printf("OK   configured TLS mode: %s\n", status.Mode)
	fmt.Printf("OK   settings source: %s\n", status.Source)
	if status.Mode == webtls.ModeManual {
		fmt.Printf("OK   certificate subject: %s\n", status.Subject)
		fmt.Printf("OK   certificate issuer: %s\n", status.Issuer)
		fmt.Printf("OK   certificate not before: %s\n", status.NotBefore.Format(time.RFC3339))
		fmt.Printf("OK   certificate not after: %s\n", status.NotAfter.Format(time.RFC3339))
	}
	if cfg.WebUITrustProxyHeaders {
		fmt.Printf("OK   trusted proxy headers: enabled (%d CIDR entries)\n", len(cfg.WebUITrustedProxyCIDRs))
	} else {
		fmt.Println("OK   trusted proxy headers: disabled")
	}
	if runtime.Source == webtls.SourceManaged {
		fmt.Println("INFO TLS variables from environment are ignored")
	}
}
