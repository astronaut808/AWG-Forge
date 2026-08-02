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
		return errors.New("usage: awg-forge tls status | tls use manual --cert <path> --key <path> [--server-name name] | tls use acme-domain --domain <name> --email <address> --accept-tos | tls use acme-ip --ip <address> --email <address> --accept-tos | tls use reverse-proxy | tls disable")
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
		return errors.New("usage: awg-forge tls status | tls use manual --cert <path> --key <path> [--server-name name] | tls use acme-domain --domain <name> --email <address> --accept-tos | tls use acme-ip --ip <address> --email <address> --accept-tos | tls use reverse-proxy | tls disable")
	}
}

func runTLSUse(cfg config.Config, svc *app.Service, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: awg-forge tls use manual --cert <path> --key <path> [--server-name name] | tls use acme-domain --domain <name> --email <address> --accept-tos | tls use reverse-proxy")
	}
	if args[0] == "acme-domain" {
		return runTLSUseACMEDomain(cfg, svc, args[1:])
	}
	if args[0] == "acme-ip" {
		return runTLSUseACMEIP(cfg, svc, args[1:])
	}
	if args[0] == "reverse-proxy" {
		if len(args) != 1 {
			return errors.New("usage: awg-forge tls use reverse-proxy")
		}
		if err := webtls.Save(cfg, webtls.Settings{Mode: webtls.ModeReverseProxy}); err != nil {
			return err
		}
		svc.Audit().Log(context.Background(), audit.Event{Level: "info", Event: "tls.reverse_proxy.configured", Message: "reverse-proxy TLS configuration saved", Fields: map[string]any{"mode": "reverse-proxy"}})
		fmt.Println("OK   reverse-proxy TLS configuration saved; restart awg-forge to apply it")
		return nil
	}
	if args[0] != "manual" {
		return errors.New("usage: awg-forge tls use manual --cert <path> --key <path> [--server-name name] | tls use acme-domain --domain <name> --email <address> --accept-tos | tls use acme-ip --ip <address> --email <address> --accept-tos | tls use reverse-proxy")
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

func runTLSUseACMEIP(cfg config.Config, svc *app.Service, args []string) error {
	flags := flag.NewFlagSet("tls use acme-ip", flag.ContinueOnError)
	ip := flags.String("ip", "", "public IPv4 or IPv6 address for ACME HTTP-01")
	email := flags.String("email", "", "ACME contact email")
	acceptTOS := flags.Bool("accept-tos", false, "accept the ACME certificate authority terms of service")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: awg-forge tls use acme-ip --ip <address> --email <address> --accept-tos")
	}
	settings := webtls.Settings{Mode: webtls.ModeACMEIP, ACMEIP: *ip, ACMEEmail: *email, ACMEAcceptTOS: *acceptTOS}
	if err := webtls.Save(cfg, settings); err != nil {
		return err
	}
	runtime, err := webtls.Load(cfg)
	if err != nil {
		return err
	}
	svc.Audit().Log(context.Background(), audit.Event{Level: "info", Event: "tls.acme_ip.configured", Message: "ACME IP TLS configuration saved", Fields: map[string]any{"mode": "acme-ip", "ip": runtime.Settings.ACMEIP}})
	fmt.Println("OK   ACME IP TLS configuration saved; restart awg-forge to request the certificate")
	return nil
}

func runTLSUseACMEDomain(cfg config.Config, svc *app.Service, args []string) error {
	flags := flag.NewFlagSet("tls use acme-domain", flag.ContinueOnError)
	domain := flags.String("domain", "", "public DNS name for ACME HTTP-01")
	email := flags.String("email", "", "ACME contact email")
	acceptTOS := flags.Bool("accept-tos", false, "accept the ACME certificate authority terms of service")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("usage: awg-forge tls use acme-domain --domain <name> --email <address> --accept-tos")
	}
	settings := webtls.Settings{Mode: webtls.ModeACMEDomain, ACMEDomain: *domain, ACMEEmail: *email, ACMEAcceptTOS: *acceptTOS}
	if err := webtls.Save(cfg, settings); err != nil {
		return err
	}
	runtime, err := webtls.Load(cfg)
	if err != nil {
		return err
	}
	svc.Audit().Log(context.Background(), audit.Event{Level: "info", Event: "tls.acme.configured", Message: "ACME TLS configuration saved", Fields: map[string]any{"mode": "acme-domain", "domain": runtime.Settings.ACMEDomain}})
	fmt.Println("OK   ACME TLS configuration saved; restart awg-forge to apply it")
	return nil
}

func printTLSStatus(cfg config.Config, runtime webtls.Runtime) {
	status := runtime.ReadStatus()
	fmt.Printf("OK   configured TLS mode: %s\n", status.Mode)
	if status.Mode == webtls.ModeManual {
		fmt.Printf("OK   certificate subject: %s\n", status.Subject)
		fmt.Printf("OK   certificate issuer: %s\n", status.Issuer)
		fmt.Printf("OK   certificate not before: %s\n", status.NotBefore.Format(time.RFC3339))
		fmt.Printf("OK   certificate not after: %s\n", status.NotAfter.Format(time.RFC3339))
	}
	if status.Mode == webtls.ModeACMEDomain {
		fmt.Printf("OK   ACME domain: %s\n", status.Domain)
		printACMEStatus(status)
	}
	if status.Mode == webtls.ModeACMEIP {
		fmt.Printf("OK   ACME IP: %s\n", status.IP)
		printACMEStatus(status)
	}
	if cfg.WebUITrustProxyHeaders {
		fmt.Printf("OK   trusted proxy headers: enabled (%d CIDR entries)\n", len(cfg.WebUITrustedProxyCIDRs))
	} else {
		fmt.Println("OK   trusted proxy headers: disabled")
	}
}

func printACMEStatus(status webtls.Status) {
	fmt.Printf("INFO certificate status: %s\n", status.State)
	if status.Error != "" {
		fmt.Printf("WARN certificate issuance: %s\n", status.Error)
	}
	if status.Warning != "" {
		fmt.Printf("WARN certificate renewal: %s\n", status.Warning)
	}
	if !status.NextAttempt.IsZero() {
		fmt.Printf("INFO next certificate attempt: %s\n", status.NextAttempt.Format(time.RFC3339))
	}
}
