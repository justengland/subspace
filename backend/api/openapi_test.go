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
}