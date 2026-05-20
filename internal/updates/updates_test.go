package updates

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/astronaut808/awg-forge/internal/buildinfo"
)

func TestCheckerReportsNewerAvailable(t *testing.T) {
	server := fakeGitHub(t, map[string]string{
		"amnezia-vpn/amneziawg-go":    "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"amnezia-vpn/amneziawg-tools": "cccccccccccccccccccccccccccccccccccccccc",
	})
	defer server.Close()

	checker := Checker{HTTPClient: server.Client(), GitHubAPI: server.URL}
	report := checker.Check(context.Background(), buildinfo.Info{
		AmneziaWGGoRepo:    buildinfo.DefaultAmneziaWGGoRepo,
		AmneziaWGToolsRepo: buildinfo.DefaultAmneziaWGToolsRepo,
		AmneziaWGGoRef:     "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		AmneziaWGToolsRef:  "cccccccccccccccccccccccccccccccccccccccc",
	})

	if got := report.Components[0].Status; got != "newer_available" {
		t.Fatalf("go status = %s, want newer_available", got)
	}
	if got := report.Components[1].Status; got != "current" {
		t.Fatalf("tools status = %s, want current", got)
	}
}

func TestCheckerKeepsComponentErrorsLocal(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		http.NotFound(rw, r)
	}))
	defer server.Close()

	checker := Checker{HTTPClient: server.Client(), GitHubAPI: server.URL}
	report := checker.Check(context.Background(), buildinfo.Current())
	if len(report.Components) != 2 {
		t.Fatalf("components = %d, want 2", len(report.Components))
	}
	for _, component := range report.Components {
		if component.Status != "unknown" || component.Error == "" {
			t.Fatalf("component = %+v, want unknown with error", component)
		}
	}
}

func fakeGitHub(t *testing.T, commits map[string]string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		for repo, sha := range commits {
			if r.URL.Path == "/repos/"+repo {
				_, _ = fmt.Fprint(rw, `{"default_branch":"master"}`)
				return
			}
			if r.URL.Path == "/repos/"+repo+"/commits/master" {
				_, _ = fmt.Fprintf(rw, `{"sha":%q}`, sha)
				return
			}
		}
		http.NotFound(rw, r)
	}))
}
