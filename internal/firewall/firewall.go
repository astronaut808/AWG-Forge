package firewall

import (
	"crypto/sha256"
	"encoding/hex"
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
	egressInterface := externalInterface
	if tunnel.EgressMode == config.EgressWarp {
		egressInterface = "warp0"
	}
	comment := func(name string) []string {
		return []string{"-m", "comment", "--comment", ruleComment(tunnel, name)}
	}
	return []Rule{
		{
			Tunnel: tunnel.Name,
			Name:   "masquerade",
			Table:  "nat",
			Chain:  "POSTROUTING",
			Args:   append([]string{"-s", tunnel.IPv4Subnet, "-o", egressInterface}, append(comment("masquerade"), "-j", "MASQUERADE")...),
			Insert: true,
		},
		{
			Tunnel: tunnel.Name,
			Name:   "input-udp",
			Chain:  "INPUT",
			Args:   append([]string{"-p", "udp", "-m", "udp", "--dport", strconv.Itoa(tunnel.ListenPort)}, append(comment("input-udp"), "-j", "ACCEPT")...),
			Insert: true,
		},
		{
			Tunnel: tunnel.Name,
			Name:   "forward-egress",
			Chain:  "FORWARD",
			Args: append(
				[]string{"-i", tunnel.InterfaceName, "-s", tunnel.IPv4Subnet, "-o", egressInterface, "-m", "conntrack", "--ctstate", "NEW,ESTABLISHED,RELATED"},
				append(comment("forward-egress"), "-j", "ACCEPT")...,
			),
			Insert: true,
		},
		{
			Tunnel: tunnel.Name,
			Name:   "forward-return",
			Chain:  "FORWARD",
			Args: append(
				[]string{"-i", egressInterface, "-o", tunnel.InterfaceName, "-d", tunnel.IPv4Subnet, "-m", "conntrack", "--ctstate", "ESTABLISHED,RELATED"},
				append(comment("forward-return"), "-j", "ACCEPT")...,
			),
			Insert: true,
		},
	}
}

func LegacyRulesForTunnel(externalInterface string, tunnel config.Tunnel) []Rule {
	egressInterface := externalInterface
	if tunnel.EgressMode == config.EgressWarp {
		egressInterface = "warp0"
	}
	return []Rule{
		{Tunnel: tunnel.Name, Name: "legacy-masquerade", Table: "nat", Chain: "POSTROUTING", Args: []string{"-s", tunnel.IPv4Subnet, "-o", egressInterface, "-j", "MASQUERADE"}},
		{Tunnel: tunnel.Name, Name: "legacy-input-udp", Chain: "INPUT", Args: []string{"-p", "udp", "-m", "udp", "--dport", strconv.Itoa(tunnel.ListenPort), "-j", "ACCEPT"}},
		{Tunnel: tunnel.Name, Name: "legacy-forward-in", Chain: "FORWARD", Args: []string{"-i", tunnel.InterfaceName, "-j", "ACCEPT"}},
		{Tunnel: tunnel.Name, Name: "legacy-forward-out", Chain: "FORWARD", Args: []string{"-o", tunnel.InterfaceName, "-j", "ACCEPT"}},
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
	errs := repairRules(ExpectedRules(cfg, state), runner)
	report := Check(cfg, state, runner)
	if len(errs) > 0 {
		return report, errors.New(strings.Join(errs, "; "))
	}
	return report, nil
}

func MigrateLegacyRules(cfg config.Config, tunnel config.Tunnel, runner Runner) (Report, error) {
	if !cfg.ApplyConfig {
		return Report{ApplyEnabled: false}, nil
	}
	state := config.State{Tunnels: []config.Tunnel{tunnel}}
	errs := repairRules(ExpectedRules(cfg, state), runner)
	if len(errs) > 0 {
		report := Check(cfg, state, runner)
		return report, errors.New(strings.Join(errs, "; "))
	}
	for _, rule := range LegacyRulesForTunnel(cfg.ExternalInterface, tunnel) {
		if err := deleteAll(rule, runner); err != nil {
			errs = append(errs, err.Error())
		}
	}
	report := Check(cfg, state, runner)
	if len(errs) > 0 {
		return report, errors.New(strings.Join(errs, "; "))
	}
	return report, nil
}

func RemoveRulesForTunnel(cfg config.Config, tunnel config.Tunnel, runner Runner) error {
	if !cfg.ApplyConfig {
		return nil
	}
	var errs []string
	for _, rule := range ExpectedRulesForTunnel(cfg.ExternalInterface, tunnel) {
		if err := deleteAll(rule, runner); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return errors.New(strings.Join(errs, "; "))
	}
	return nil
}

func repairRules(rules []Rule, runner Runner) []string {
	var errs []string
	for _, rule := range rules {
		count, err := runner.Count(rule)
		if err != nil {
			errs = append(errs, err.Error())
			continue
		}
		if count == 0 {
			if err := runner.Insert(rule); err != nil {
				errs = append(errs, err.Error())
			}
			continue
		}
		for count > 1 {
			if err := runner.Delete(rule); err != nil {
				errs = append(errs, err.Error())
				break
			}
			count--
		}
	}
	return errs
}

func deleteAll(rule Rule, runner Runner) error {
	for i := 0; i < 64; i++ {
		count, err := runner.Count(rule)
		if err != nil {
			return err
		}
		if count == 0 {
			return nil
		}
		if err := runner.Delete(rule); err != nil {
			return err
		}
	}
	return errors.New("firewall duplicate cleanup limit reached for " + rule.Spec())
}

func ruleComment(tunnel config.Tunnel, rule string) string {
	owner := tunnel.ID
	if !iptablesSafeToken(owner) || len("awg-forge-"+owner+"-"+rule) > 255 {
		digest := sha256.Sum256([]byte(tunnel.ID + "\x00" + tunnel.InterfaceName))
		owner = hex.EncodeToString(digest[:8])
	}
	return "awg-forge-" + owner + "-" + rule
}

func iptablesSafeToken(value string) bool {
	if value == "" {
		return false
	}
	for _, c := range value {
		if c != '-' && c != '_' && (c < '0' || c > '9') && (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') {
			return false
		}
	}
	return true
}

func (r Rule) Spec() string {
	prefix := ""
	if r.Table != "" {
		prefix = "-t " + r.Table + " "
	}
	return prefix + r.Chain + " " + strings.Join(r.Args, " ")
}

type IPTablesRunner struct {
	output func(args ...string) ([]byte, error)
	run    func(args ...string) error
}

func (r IPTablesRunner) Count(rule Rule) (int, error) {
	comment, managed := ruleCommentFromArgs(rule.Args)
	if !managed {
		exists, err := r.exists(rule)
		if err != nil || !exists {
			return 0, err
		}
		return 1, nil
	}
	args := append(tableArgs(rule.Table), "-S", rule.Chain)
	out, err := r.commandOutput(args...)
	if err != nil {
		return 0, fmt.Errorf("iptables %s failed: %w", strings.Join(args, " "), err)
	}
	count := 0
	for _, line := range strings.Split(string(out), "\n") {
		if lineHasComment(line, comment) {
			count++
		}
	}
	if count == 0 {
		return 0, nil
	}
	exists, err := r.exists(rule)
	if err != nil {
		return 0, err
	}
	if !exists {
		return 0, fmt.Errorf("managed firewall tag %q conflicts with the expected rule", comment)
	}
	return count, nil
}

func (r IPTablesRunner) Delete(rule Rule) error {
	args := append(tableArgs(rule.Table), "-D", rule.Chain)
	args = append(args, rule.Args...)
	err := r.commandRun(args...)
	if err != nil {
		return fmt.Errorf("iptables %s failed: %w", strings.Join(args, " "), err)
	}
	return nil
}

func (r IPTablesRunner) Insert(rule Rule) error {
	action := "-A"
	if rule.Insert {
		action = "-I"
	}
	args := append(tableArgs(rule.Table), action, rule.Chain)
	if rule.Insert {
		args = append(args, "1")
	}
	args = append(args, rule.Args...)
	err := r.commandRun(args...)
	if err != nil {
		return fmt.Errorf("iptables %s failed: %w", strings.Join(args, " "), err)
	}
	return nil
}

func (r IPTablesRunner) exists(rule Rule) (bool, error) {
	args := append(tableArgs(rule.Table), "-C", rule.Chain)
	args = append(args, rule.Args...)
	err := r.commandRun(args...)
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return false, nil
	}
	return false, fmt.Errorf("iptables %s failed: %w", strings.Join(args, " "), err)
}

func (r IPTablesRunner) commandOutput(args ...string) ([]byte, error) {
	if r.output != nil {
		return r.output(args...)
	}
	return exec.Command("iptables", args...).Output()
}

func (r IPTablesRunner) commandRun(args ...string) error {
	if r.run != nil {
		return r.run(args...)
	}
	return exec.Command("iptables", args...).Run()
}

func ruleCommentFromArgs(args []string) (string, bool) {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--comment" {
			return args[i+1], true
		}
	}
	return "", false
}

func lineHasComment(line, comment string) bool {
	fields := strings.Fields(line)
	for i := 0; i+1 < len(fields); i++ {
		if fields[i] == "--comment" && fields[i+1] == comment {
			return true
		}
	}
	return false
}

func tableArgs(table string) []string {
	if table == "" {
		return nil
	}
	return []string{"-t", table}
}
