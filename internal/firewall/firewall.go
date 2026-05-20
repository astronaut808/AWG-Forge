package firewall

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/astronaut808/awg-forge/internal/config"
)

type Rule struct {
	Tunnel string
	Name   string
	Table  string
	Chain  string
	Args   []string
	Insert bool
}

type RuleReport struct {
	Tunnel  string `json:"tunnel"`
	Rule    string `json:"rule"`
	Table   string `json:"table"`
	Chain   string `json:"chain"`
	Spec    string `json:"spec"`
	Count   int    `json:"count"`
	Status  string `json:"status"`
	Message string `json:"message,omitempty"`
}

type Report struct {
	ApplyEnabled bool         `json:"apply_enabled"`
	Results      []RuleReport `json:"results"`
}

type Runner interface {
	Count(rule Rule) (int, error)
	Delete(rule Rule) error
	Insert(rule Rule) error
}

func ExpectedRules(cfg config.Config, state config.State) []Rule {
	var out []Rule
	for _, tunnel := range state.Tunnels {
		if !tunnel.Enabled {
			continue
		}
		out = append(out, ExpectedRulesForTunnel(cfg.ExternalInterface, tunnel)...)
	}
	return out
}

func ExpectedRulesForTunnel(externalInterface string, tunnel config.Tunnel) []Rule {
	return []Rule{
		{
			Tunnel: tunnel.Name,
			Name:   "masquerade",
			Table:  "nat",
			Chain:  "POSTROUTING",
			Args:   []string{"-s", tunnel.IPv4Subnet, "-o", externalInterface, "-j", "MASQUERADE"},
			Insert: true,
		},
		{
			Tunnel: tunnel.Name,
			Name:   "input-udp",
			Chain:  "INPUT",
			Args:   []string{"-p", "udp", "-m", "udp", "--dport", strconv.Itoa(tunnel.ListenPort), "-j", "ACCEPT"},
			Insert: true,
		},
		{
			Tunnel: tunnel.Name,
			Name:   "forward-in",
			Chain:  "FORWARD",
			Args:   []string{"-i", tunnel.InterfaceName, "-j", "ACCEPT"},
			Insert: true,
		},
		{
			Tunnel: tunnel.Name,
			Name:   "forward-out",
			Chain:  "FORWARD",
			Args:   []string{"-o", tunnel.InterfaceName, "-j", "ACCEPT"},
			Insert: true,
		},
	}
}

func Check(cfg config.Config, state config.State, runner Runner) Report {
	report := Report{ApplyEnabled: cfg.ApplyConfig}
	if !cfg.ApplyConfig {
		return report
	}
	for _, rule := range ExpectedRules(cfg, state) {
		item := RuleReport{
			Tunnel: rule.Tunnel,
			Rule:   rule.Name,
			Table:  rule.Table,
			Chain:  rule.Chain,
			Spec:   rule.Spec(),
		}
		count, err := runner.Count(rule)
		item.Count = count
		switch {
		case err != nil:
			item.Status = "error"
			item.Message = err.Error()
		case count == 0:
			item.Status = "missing"
			item.Message = "run awg-forge firewall repair"
		case count == 1:
			item.Status = "ok"
		default:
			item.Status = "duplicate"
			item.Message = fmt.Sprintf("found %d copies; run awg-forge firewall repair", count)
		}
		report.Results = append(report.Results, item)
	}
	return report
}

func Repair(cfg config.Config, state config.State, runner Runner) (Report, error) {
	if !cfg.ApplyConfig {
		return Report{ApplyEnabled: false}, nil
	}
	var errs []string
	for _, rule := range ExpectedRules(cfg, state) {
		cleaned := false
		failed := false
		for i := 0; i < 64; i++ {
			count, err := runner.Count(rule)
			if err != nil {
				errs = append(errs, err.Error())
				cleaned = true
				failed = true
				break
			}
			if count == 0 {
				cleaned = true
				break
			}
			if err := runner.Delete(rule); err != nil {
				errs = append(errs, err.Error())
				cleaned = true
				failed = true
				break
			}
		}
		if failed {
			continue
		}
		if !cleaned {
			errs = append(errs, "firewall duplicate cleanup limit reached for "+rule.Spec())
			continue
		}
		if err := runner.Insert(rule); err != nil {
			errs = append(errs, err.Error())
		}
	}
	report := Check(cfg, state, runner)
	if len(errs) > 0 {
		return report, errors.New(strings.Join(errs, "; "))
	}
	return report, nil
}

func (r Rule) Spec() string {
	prefix := ""
	if r.Table != "" {
		prefix = "-t " + r.Table + " "
	}
	return prefix + r.Chain + " " + strings.Join(r.Args, " ")
}

type IPTablesRunner struct{}

func (IPTablesRunner) Count(rule Rule) (int, error) {
	args := append(tableArgs(rule.Table), "-S", rule.Chain)
	out, err := exec.Command("iptables", args...).CombinedOutput()
	if err != nil {
		return 0, fmt.Errorf("iptables %s failed: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	want := "-A " + rule.Chain + " " + strings.Join(rule.Args, " ")
	count := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.TrimSpace(line) == want {
			count++
		}
	}
	return count, nil
}

func (IPTablesRunner) Delete(rule Rule) error {
	args := append(tableArgs(rule.Table), "-D", rule.Chain)
	args = append(args, rule.Args...)
	out, err := exec.Command("iptables", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("iptables %s failed: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}

func (IPTablesRunner) Insert(rule Rule) error {
	action := "-A"
	if rule.Insert {
		action = "-I"
	}
	args := append(tableArgs(rule.Table), action, rule.Chain)
	if rule.Insert {
		args = append(args, "1")
	}
	args = append(args, rule.Args...)
	out, err := exec.Command("iptables", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("iptables %s failed: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
	return nil
}

func tableArgs(table string) []string {
	if table == "" {
		return nil
	}
	return []string{"-t", table}
}
