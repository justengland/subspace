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
		"/api/runs:",
		"/api/runs/{id}:",
		"/api/runs/{id}/pause:",
		"/api/runs/{id}/resume:",
		"/api/runs/{id}/stop:",
		"/api/runs/{id}/rewind:",
		"/api/runs/{id}/events:",
		"StartRequest:",
		"inputOverrides:",
		"argumentOverrides:",
		"operationId: listRuns",
	} {
		if !strings.Contains(doc, path) {
			t.Fatalf("openapi.yaml missing %q", path)
		}
	}
}