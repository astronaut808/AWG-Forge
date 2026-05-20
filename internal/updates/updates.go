package updates

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/astronaut808/awg-forge/internal/buildinfo"
)

const defaultGitHubAPI = "https://api.github.com"

type ComponentStatus struct {
	Name          string `json:"name"`
	Repository    string `json:"repository"`
	CurrentRef    string `json:"current_ref"`
	LatestRef     string `json:"latest_ref"`
	DefaultBranch string `json:"default_branch"`
	Status        string `json:"status"`
	Error         string `json:"error,omitempty"`
}

type Report struct {
	BuildInfo  buildinfo.Info    `json:"build_info"`
	Components []ComponentStatus `json:"components"`
}

type Checker struct {
	HTTPClient *http.Client
	GitHubAPI  string
}

func NewChecker() Checker {
	return Checker{
		HTTPClient: &http.Client{Timeout: 8 * time.Second},
		GitHubAPI:  defaultGitHubAPI,
	}
}

func Check(ctx context.Context) Report {
	return NewChecker().Check(ctx, buildinfo.Current())
}

func (c Checker) Check(ctx context.Context, info buildinfo.Info) Report {
	report := Report{BuildInfo: info}
	report.Components = append(report.Components,
		c.checkRepo(ctx, "amneziawg-go", info.AmneziaWGGoRepo, info.AmneziaWGGoRef),
		c.checkRepo(ctx, "amneziawg-tools", info.AmneziaWGToolsRepo, info.AmneziaWGToolsRef),
	)
	return report
}

func (c Checker) checkRepo(ctx context.Context, name, repo, current string) ComponentStatus {
	status := ComponentStatus{Name: name, Repository: repo, CurrentRef: current, Status: "unknown"}
	repoInfo, err := c.fetchRepo(ctx, repo)
	if err != nil {
		status.Error = err.Error()
		return status
	}
	status.DefaultBranch = repoInfo.DefaultBranch
	latest, err := c.fetchCommit(ctx, repo, repoInfo.DefaultBranch)
	if err != nil {
		status.Error = err.Error()
		return status
	}
	status.LatestRef = latest.SHA
	if sameRef(current, latest.SHA) {
		status.Status = "current"
	} else {
		status.Status = "newer_available"
	}
	return status
}

type githubRepo struct {
	DefaultBranch string `json:"default_branch"`
}

type githubCommit struct {
	SHA string `json:"sha"`
}

func (c Checker) fetchRepo(ctx context.Context, repo string) (githubRepo, error) {
	var out githubRepo
	if err := c.getJSON(ctx, "/repos/"+repo, &out); err != nil {
		return out, err
	}
	if out.DefaultBranch == "" {
		return out, fmt.Errorf("default branch unavailable for %s", repo)
	}
	return out, nil
}

func (c Checker) fetchCommit(ctx context.Context, repo, ref string) (githubCommit, error) {
	var out githubCommit
	if err := c.getJSON(ctx, "/repos/"+repo+"/commits/"+ref, &out); err != nil {
		return out, err
	}
	if out.SHA == "" {
		return out, fmt.Errorf("latest commit unavailable for %s", repo)
	}
	return out, nil
}

func (c Checker) getJSON(ctx context.Context, path string, dst any) error {
	client := c.HTTPClient
	if client == nil {
		client = http.DefaultClient
	}
	base := strings.TrimRight(c.GitHubAPI, "/")
	if base == "" {
		base = defaultGitHubAPI
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "awg-forge-update-checker")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("github api %s returned %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}

func sameRef(current, latest string) bool {
	current = strings.TrimSpace(strings.ToLower(current))
	latest = strings.TrimSpace(strings.ToLower(latest))
	return current != "" && (current == latest || strings.HasPrefix(latest, current) || strings.HasPrefix(current, latest))
}
