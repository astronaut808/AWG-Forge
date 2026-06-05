package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/astronaut808/awg-forge/internal/app"
	"github.com/astronaut808/awg-forge/internal/audit"
	"github.com/astronaut808/awg-forge/internal/backup"
	"github.com/astronaut808/awg-forge/internal/config"
	"github.com/astronaut808/awg-forge/internal/doctor"
	"github.com/astronaut808/awg-forge/internal/firewall"
	"github.com/astronaut808/awg-forge/internal/server"
	"github.com/astronaut808/awg-forge/internal/support"
	"github.com/astronaut808/awg-forge/internal/updates"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usage()
	}
	if args[0] == "updates" {
		return runUpdates()
	}
	cfg, err := config.FromEnv()
	if err != nil {
		return err
	}
	svc := app.New(cfg)

	switch args[0] {
	case "init":
		_, err := svc.Init()
		return err
	case "serve":
		if _, err := svc.Init(); err != nil {
			return err
		}
		if err := svc.RenderAll(); err != nil {
			return err
		}
		return server.Serve(cfg, svc)
	case "render":
		return svc.RenderAll()
	case "doctor":
		return doctor.Run(cfg, svc)
	case "backup":
		return runBackup(cfg, svc, args[1:])
	case "restore":
		return runRestore(cfg, args[1:])
	case "support-bundle":
		return runSupportBundle(cfg, svc, args[1:])
	case "firewall":
		return runFirewall(svc, args[1:])
	case "logs":
		return runLogs(cfg, args[1:])
	case "client":
		return runClient(svc, args[1:])
	case "tunnel":
		return runTunnel(svc, args[1:])
	default:
		return usage()
	}
}

func runTunnel(svc *app.Service, args []string) error {
	if len(args) == 1 && args[0] == "restart" {
		return svc.RestartTunnel()
	}
	if len(args) >= 2 && args[0] == "create" {
		profileID := args[1]
		name, port, subnet := app.SuggestedTunnelSpec(profileID)
		if len(args) > 2 {
			name = args[2]
		}
		if len(args) > 3 {
			n, err := strconv.Atoi(args[3])
			if err != nil {
				return err
			}
			port = n
		}
		if len(args) > 4 {
			subnet = args[4]
		}
		tunnel, err := svc.CreateTunnel(profileID, name, subnet, port)
		if err != nil {
			return err
		}
		fmt.Println(tunnel.ID)
		return nil
	}
	return errors.New("usage: awg-forge tunnel restart | tunnel create <profile> [name] [port] [subnet]")
}

func runClient(svc *app.Service, args []string) error {
	if len(args) < 1 {
		return usage()
	}
	switch args[0] {
	case "add":
		if len(args) < 2 || len(args) > 3 {
			return errors.New("usage: awg-forge client add <name> [tunnel-id|name|interface]")
		}
		var (
			client config.Client
			err    error
		)
		if len(args) == 3 {
			client, err = svc.AddClientToTunnel(args[2], args[1])
		} else {
			client, err = svc.AddClient(args[1])
		}
		if err != nil {
			return err
		}
		fmt.Println(client.ID)
		return nil
	case "remove":
		if len(args) != 2 {
			return errors.New("usage: awg-forge client remove <id>")
		}
		return svc.RemoveClient(args[1])
	case "enable":
		if len(args) != 2 {
			return errors.New("usage: awg-forge client enable <id>")
		}
		return svc.SetClientEnabled(args[1], true)
	case "disable":
		if len(args) != 2 {
			return errors.New("usage: awg-forge client disable <id>")
		}
		return svc.SetClientEnabled(args[1], false)
	case "config":
		if len(args) != 2 {
			return errors.New("usage: awg-forge client config <id>")
		}
		conf, err := svc.ClientConfig(args[1])
		if err != nil {
			return err
		}
		fmt.Print(conf)
		return nil
	default:
		return usage()
	}
}

func usage() error {
	return errors.New("usage: awg-forge init|serve|render|doctor|backup|restore|support-bundle|updates|firewall|logs|client|tunnel")
}

func runFirewall(svc *app.Service, args []string) error {
	if len(args) != 1 {
		return errors.New("usage: awg-forge firewall check|repair")
	}
	var (
		report firewall.Report
		err    error
	)
	switch args[0] {
	case "check":
		report, err = svc.FirewallCheck()
	case "repair":
		report, err = svc.FirewallRepair()
	default:
		return errors.New("usage: awg-forge firewall check|repair")
	}
	if err != nil {
		printFirewallReport(report)
		return err
	}
	printFirewallReport(report)
	return nil
}

func printFirewallReport(report firewall.Report) {
	if !report.ApplyEnabled {
		fmt.Println("WARN firewall: APPLY_CONFIG=false; runtime firewall rules are not managed")
		return
	}
	if len(report.Results) == 0 {
		fmt.Println("OK   firewall: no enabled tunnels")
		return
	}
	for _, result := range report.Results {
		level := "OK"
		if result.Status == "missing" || result.Status == "error" {
			level = "FAIL"
		}
		if result.Status == "duplicate" {
			level = "WARN"
		}
		fmt.Printf("%-4s firewall %s/%s: %s", level, result.Tunnel, result.Rule, result.Status)
		if result.Count != 1 {
			fmt.Printf(" (%d)", result.Count)
		}
		if result.Message != "" {
			fmt.Printf("; %s", result.Message)
		}
		fmt.Println()
	}
}

func runBackup(cfg config.Config, svc *app.Service, args []string) error {
	if len(args) > 1 {
		return errors.New("usage: BACKUP_PASSWORD=... awg-forge backup [output.afbackup]")
	}
	password := os.Getenv("BACKUP_PASSWORD")
	if password == "" {
		return errors.New("BACKUP_PASSWORD is required")
	}
	path := ""
	if len(args) == 1 {
		path = args[0]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	written, err := backup.WriteFile(ctx, cfg, svc, password, path)
	if err != nil {
		audit.New(cfg).Log(context.Background(), audit.Event{Level: "error", Event: "backup.create.failed", Message: "encrypted backup creation failed", Error: audit.Error(err)})
		return err
	}
	audit.New(cfg).Log(context.Background(), audit.Event{Level: "info", Event: "backup.created", Message: "encrypted backup created", Fields: map[string]any{"path": written}})
	fmt.Println(written)
	return nil
}

func runRestore(cfg config.Config, args []string) error {
	if len(args) == 2 && args[0] == "verify" {
		return runRestoreVerify(cfg, args[1])
	}
	if len(args) != 1 {
		return errors.New("usage: BACKUP_PASSWORD=... awg-forge restore <backup.afbackup> | restore verify <backup.afbackup>")
	}
	password := os.Getenv("BACKUP_PASSWORD")
	if password == "" {
		return errors.New("BACKUP_PASSWORD is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := backup.Restore(ctx, cfg, password, args[0]); err != nil {
		audit.New(cfg).Log(context.Background(), audit.Event{Level: "error", Event: "restore.failed", Message: "encrypted backup restore failed", Fields: map[string]any{"path": args[0]}, Error: audit.Error(err)})
		return err
	}
	audit.New(cfg).Log(context.Background(), audit.Event{Level: "info", Event: "restore.completed", Message: "encrypted backup restored", Fields: map[string]any{"path": args[0]}})
	return nil
}

func runRestoreVerify(cfg config.Config, path string) error {
	password := os.Getenv("BACKUP_PASSWORD")
	if password == "" {
		return errors.New("BACKUP_PASSWORD is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	report, err := backup.Verify(ctx, cfg, password, path)
	if err != nil {
		audit.New(cfg).Log(context.Background(), audit.Event{Level: "error", Event: "restore.verify.failed", Message: "backup verification failed", Fields: map[string]any{"path": path}, Error: audit.Error(err)})
		return err
	}
	audit.New(cfg).Log(context.Background(), audit.Event{Level: "info", Event: "restore.verified", Message: "backup verified", Fields: map[string]any{"path": path, "tunnels": len(report.Tunnels), "clients": report.ClientCount}})
	printRestoreVerifyReport(report)
	return nil
}

func printRestoreVerifyReport(report backup.VerifyReport) {
	fmt.Println("OK   backup: decrypted and verified")
	fmt.Printf("OK   format: %s\n", report.Format)
	fmt.Printf("OK   schema: %d\n", report.SchemaVersion)
	if report.CreatedAt != "" {
		fmt.Printf("OK   created: %s\n", report.CreatedAt)
	}
	if report.Build.Version != "" || report.Build.Commit != "" {
		fmt.Printf("OK   build: version=%s commit=%s\n", emptyDash(report.Build.Version), emptyDash(report.Build.Commit))
	}
	fmt.Printf("OK   files: %d verified, %s total\n", report.FileCount, formatBytes(report.TotalSize))
	fmt.Printf("OK   tunnels: %d\n", len(report.Tunnels))
	fmt.Printf("OK   clients: %d\n", report.ClientCount)
	if report.ServerHost != "" {
		fmt.Printf("OK   server host: %s\n", report.ServerHost)
	}
	if len(report.Tunnels) == 0 {
		return
	}
	fmt.Println()
	fmt.Println("Tunnels:")
	for _, tunnel := range report.Tunnels {
		fmt.Printf("- %-12s %-16s %-15s %-18s %5d/udp %d clients\n",
			tunnel.Name,
			tunnel.Profile,
			tunnel.Interface,
			tunnel.Subnet,
			tunnel.ListenPort,
			tunnel.Clients,
		)
	}
}

func emptyDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func formatBytes(n int64) string {
	const unit = int64(1024)
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := unit, 0
	for n >= div*unit && exp < 4 {
		div *= unit
		exp++
	}
	value := float64(n) / float64(div)
	return fmt.Sprintf("%.1f %ciB", value, "KMGTPE"[exp])
}

func runSupportBundle(cfg config.Config, svc *app.Service, args []string) error {
	if len(args) > 1 {
		return errors.New("usage: awg-forge support-bundle [output.zip]")
	}
	path := ""
	if len(args) == 1 {
		path = args[0]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	written, err := support.WriteFile(ctx, cfg, svc, path)
	if err != nil {
		audit.New(cfg).Log(context.Background(), audit.Event{Level: "error", Event: "support_bundle.failed", Message: "support bundle creation failed", Error: audit.Error(err)})
		return err
	}
	audit.New(cfg).Log(context.Background(), audit.Event{Level: "info", Event: "support_bundle.created", Message: "support bundle created", Fields: map[string]any{"path": written}})
	fmt.Println(written)
	return nil
}

func runLogs(cfg config.Config, args []string) error {
	opts := audit.ReadOptions{Tail: audit.DefaultTail}
	jsonOutput := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--tail":
			if i+1 >= len(args) {
				return errors.New("usage: awg-forge logs [--tail N] [--level info|warn|error] [--event name] [--json]")
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil {
				return err
			}
			opts.Tail = n
			i++
		case "--level":
			if i+1 >= len(args) {
				return errors.New("usage: awg-forge logs [--tail N] [--level info|warn|error] [--event name] [--json]")
			}
			opts.Level = args[i+1]
			i++
		case "--event":
			if i+1 >= len(args) {
				return errors.New("usage: awg-forge logs [--tail N] [--level info|warn|error] [--event name] [--json]")
			}
			opts.Event = args[i+1]
			i++
		case "--json":
			jsonOutput = true
		default:
			return errors.New("usage: awg-forge logs [--tail N] [--level info|warn|error] [--event name] [--json]")
		}
	}
	events, err := audit.ReadFile(cfg.AuditLogPath, opts)
	if err != nil {
		return err
	}
	if jsonOutput {
		for _, event := range events {
			b, err := json.Marshal(event)
			if err != nil {
				return err
			}
			fmt.Println(string(b))
		}
		return nil
	}
	fmt.Print(audit.FormatText(events))
	return nil
}

func runUpdates() error {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	report := updates.Check(ctx)
	info := report.BuildInfo
	fmt.Printf("awg-forge: %s (%s)\n", info.Version, info.Commit)
	fmt.Println("AmneziaWG update mode: manual")
	for _, component := range report.Components {
		fmt.Printf("\n%s\n", component.Name)
		fmt.Printf("  repository: %s\n", component.Repository)
		fmt.Printf("  pinned:     %s\n", shortRef(component.CurrentRef))
		if component.Error != "" {
			fmt.Printf("  status:     unknown (%s)\n", component.Error)
			continue
		}
		fmt.Printf("  upstream:   %s (%s)\n", shortRef(component.LatestRef), component.DefaultBranch)
		fmt.Printf("  status:     %s\n", strings.ReplaceAll(component.Status, "_", " "))
	}
	return nil
}

func shortRef(ref string) string {
	ref = strings.TrimSpace(ref)
	if len(ref) > 12 {
		return ref[:12]
	}
	if ref == "" {
		return "unknown"
	}
	return ref
}
