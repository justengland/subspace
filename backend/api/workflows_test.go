package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/justengland/subspace/backend/api"
	"github.com/justengland/subspace/backend/engine"
	"github.com/justengland/subspace/backend/registry"
)

func TestHTTPListAndGetWorkflowsByRepo(t *testing.T) {
	home := t.TempDir()
	projA := filepath.Join(home, "proj-a")
	projB := filepath.Join(home, "proj-b")
	mustMkdir(t, projA)
	mustMkdir(t, projB)
	if err := registry.Add(home, "alpha", projA); err != nil {
		t.Fatal(err)
	}
	if err := registry.Add(home, "beta", projB); err != nil {
		t.Fatal(err)
	}

	writeWorkflow(t, home, "alpha", "run-tests", `
id: run-tests
name: Run Tests Alpha
steps:
  - id: s
    mode: series
    processes:
      - id: p
        command: true
`)
	writeWorkflow(t, home, "beta", "run-tests", `
id: run-tests
name: Run Tests Beta
steps:
  - id: s
    mode: series
    processes:
      - id: p
        command: true
`)
	writeWorkflow(t, home, "alpha", "deploy", `
id: deploy
name: Deploy
steps:
  - id: s
    mode: series
    processes:
      - id: p
        command: true
`)

	eng := engine.New(engine.Config{StorageRoot: home})
	h := (&api.Server{Eng: eng, Home: home}).Handler()

	// list alpha
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/workflows/alpha", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("list alpha status %d: %s", rr.Code, rr.Body)
	}
	var alphaList []engine.Workflow
	if err := json.Unmarshal(rr.Body.Bytes(), &alphaList); err != nil {
		t.Fatal(err)
	}
	if len(alphaList) != 2 {
		t.Fatalf("alpha list len=%d want 2: %+v", len(alphaList), alphaList)
	}
	ids := map[string]string{}
	for _, w := range alphaList {
		ids[w.ID] = w.Name
	}
	if ids["run-tests"] != "Run Tests Alpha" || ids["deploy"] != "Deploy" {
		t.Fatalf("alpha workflows=%+v", ids)
	}

	// get alpha/run-tests
	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, httptest.NewRequest(http.MethodGet, "/api/workflows/alpha/run-tests", nil))
	if rr2.Code != http.StatusOK {
		t.Fatalf("get alpha status %d: %s", rr2.Code, rr2.Body)
	}
	var wf engine.Workflow
	if err := json.Unmarshal(rr2.Body.Bytes(), &wf); err != nil {
		t.Fatal(err)
	}
	if wf.ID != "run-tests" || wf.Name != "Run Tests Alpha" {
		t.Fatalf("got %+v", wf)
	}

	// cross-repo isolation: beta list must not include alpha's deploy
	rr3 := httptest.NewRecorder()
	h.ServeHTTP(rr3, httptest.NewRequest(http.MethodGet, "/api/workflows/beta", nil))
	if rr3.Code != http.StatusOK {
		t.Fatalf("list beta status %d: %s", rr3.Code, rr3.Body)
	}
	var betaList []engine.Workflow
	if err := json.Unmarshal(rr3.Body.Bytes(), &betaList); err != nil {
		t.Fatal(err)
	}
	if len(betaList) != 1 || betaList[0].ID != "run-tests" || betaList[0].Name != "Run Tests Beta" {
		t.Fatalf("beta list=%+v", betaList)
	}

	// unknown repo
	rr4 := httptest.NewRecorder()
	h.ServeHTTP(rr4, httptest.NewRequest(http.MethodGet, "/api/workflows/missing", nil))
	if rr4.Code != http.StatusNotFound {
		t.Fatalf("unknown repo status %d want 404: %s", rr4.Code, rr4.Body)
	}

	// unknown workflow
	rr5 := httptest.NewRecorder()
	h.ServeHTTP(rr5, httptest.NewRequest(http.MethodGet, "/api/workflows/alpha/nope", nil))
	if rr5.Code != http.StatusNotFound {
		t.Fatalf("unknown workflow status %d want 404: %s", rr5.Code, rr5.Body)
	}

	// flat-by-id path gone (would have matched as repo "pipe" → 404 unknown repo)
	rr6 := httptest.NewRecorder()
	h.ServeHTTP(rr6, httptest.NewRequest(http.MethodGet, "/api/workflows/pipe", nil))
	if rr6.Code != http.StatusNotFound {
		t.Fatalf("flat id status %d want 404: %s", rr6.Code, rr6.Body)
	}
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeWorkflow(t *testing.T, home, repo, id, yaml string) {
	t.Helper()
	dir := filepath.Join(home, repo, "workflows")
	mustMkdir(t, dir)
	if err := os.WriteFile(filepath.Join(dir, id+".yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
}
