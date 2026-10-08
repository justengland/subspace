package api_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Ensures openapi.yaml still names every HTTP control-plane route.
func TestOpenAPICoversControlPlane(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	yamlPath := filepath.Join(filepath.Dir(file), "..", "..", "openapi", "openapi.yaml")
	b, err := os.ReadFile(yamlPath)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	for _, path := range []string{
		"/api/repos:",
		"/api/workflows/{repo}:",
		"/api/workflows/{repo}/{workflowId}:",
		"/api/runs/{repo}:",
		"/api/runs/{repo}/{runId}:",
		"/api/runs/{repo}/{runId}/pause:",
		"/api/runs/{repo}/{runId}/resume:",
		"/api/runs/{repo}/{runId}/stop:",
		"/api/runs/{repo}/{runId}/rewind:",
		"/api/runs/{repo}/{runId}/events:",
		"StartRequest:",
		"Repo:",
		"inputOverrides:",
		"argumentOverrides:",
		"operationId: listRuns",
		"operationId: listRepos",
		"operationId: listWorkflows",
	} {
		if !strings.Contains(doc, path) {
			t.Fatalf("openapi.yaml missing %q", path)
		}
	}
	for _, banned := range []string{
		"/api/directory",
		"\n  /api/workflows:\n",
		"\n  /api/runs:\n",
	} {
		if strings.Contains(doc, banned) {
			t.Fatalf("openapi.yaml must not define aggregate path %q", strings.TrimSpace(banned))
		}
	}
}

func TestADRsRepoScopedStorage(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller")
	}
	adrDir := filepath.Join(filepath.Dir(file), "..", "..", "docs", "adr")
	old, err := os.ReadFile(filepath.Join(adrDir, "0001-definitions-vs-project-runs.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(old), "Superseded by ADR-0003") {
		t.Fatal("ADR-0001 must declare supersession by ADR-0003")
	}
	neu, err := os.ReadFile(filepath.Join(adrDir, "0003-repo-scoped-workflows-under-home.md"))
	if err != nil {
		t.Fatal(err)
	}
	doc := string(neu)
	for _, want := range []string{
		"repos.json",
		"Subspace home",
		"Workflow",
		"Repo",
	} {
		if !strings.Contains(doc, want) {
			t.Fatalf("ADR-0003 missing %q", want)
		}
	}
}