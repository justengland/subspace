package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/justengland/subspace/backend/api"
	"github.com/justengland/subspace/backend/engine"
	"github.com/justengland/subspace/backend/registry"
)

func TestHTTPNestedRunsStartGetListIsolation(t *testing.T) {
	home := t.TempDir()
	projA := filepath.Join(home, "proj-a")
	projB := filepath.Join(home, "proj-b")
	mustMkdir(t, projA)
	mustMkdir(t, projB)
	_ = os.WriteFile(filepath.Join(projA, "ok.sh"), []byte("#!/bin/sh\necho ok\n"), 0o755)
	_ = os.WriteFile(filepath.Join(projB, "ok.sh"), []byte("#!/bin/sh\necho ok\n"), 0o755)
	if err := registry.Add(home, "alpha", projA); err != nil {
		t.Fatal(err)
	}
	if err := registry.Add(home, "beta", projB); err != nil {
		t.Fatal(err)
	}
	writeWorkflow(t, home, "alpha", "one", `
id: one
steps:
  - id: s
    mode: series
    processes:
      - id: p
        command: ./ok.sh
`)
	writeWorkflow(t, home, "beta", "one", `
id: one
steps:
  - id: s
    mode: series
    processes:
      - id: p
        command: ./ok.sh
`)

	eng := engine.New(engine.Config{StorageRoot: home})
	h := (&api.Server{Eng: eng, Home: home}).Handler()

	body, _ := json.Marshal(map[string]string{"workflowId": "one"})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/runs/alpha", bytes.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("start alpha status %d: %s", rr.Code, rr.Body)
	}
	var run engine.WorkflowRun
	if err := json.Unmarshal(rr.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	if run.ID == "" || run.WorkflowID != "one" {
		t.Fatalf("run=%+v", run)
	}
	if run.ProjectPath != projA {
		t.Fatalf("projectPath=%q want %q", run.ProjectPath, projA)
	}
	runDir := filepath.Join(home, "alpha", run.ID)
	if _, err := os.Stat(filepath.Join(runDir, "events.jsonl")); err != nil {
		t.Fatalf("artifacts under <home>/<repo>/<run-id>/: %v", err)
	}

	// wait succeeded
	deadline := time.Now().Add(5 * time.Second)
	for {
		rrG := httptest.NewRecorder()
		h.ServeHTTP(rrG, httptest.NewRequest(http.MethodGet, "/api/runs/alpha/"+run.ID, nil))
		if rrG.Code != http.StatusOK {
			t.Fatalf("get status %d: %s", rrG.Code, rrG.Body)
		}
		_ = json.Unmarshal(rrG.Body.Bytes(), &run)
		if run.Status == engine.StatusSucceeded {
			break
		}
		if run.Status != engine.StatusRunning {
			t.Fatalf("get=%+v", run)
		}
		if time.Now().After(deadline) {
			t.Fatal("timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// start beta run
	rrB := httptest.NewRecorder()
	h.ServeHTTP(rrB, httptest.NewRequest(http.MethodPost, "/api/runs/beta", bytes.NewReader(body)))
	if rrB.Code != http.StatusOK {
		t.Fatalf("start beta status %d: %s", rrB.Code, rrB.Body)
	}
	var betaRun engine.WorkflowRun
	_ = json.Unmarshal(rrB.Body.Bytes(), &betaRun)

	// list alpha: only alpha's run
	rrL := httptest.NewRecorder()
	h.ServeHTTP(rrL, httptest.NewRequest(http.MethodGet, "/api/runs/alpha", nil))
	if rrL.Code != http.StatusOK {
		t.Fatalf("list alpha %d: %s", rrL.Code, rrL.Body)
	}
	var alphaRuns []engine.WorkflowRun
	if err := json.Unmarshal(rrL.Body.Bytes(), &alphaRuns); err != nil {
		t.Fatal(err)
	}
	if len(alphaRuns) != 1 || alphaRuns[0].ID != run.ID {
		t.Fatalf("alpha runs=%+v", alphaRuns)
	}

	// cross-repo: alpha get cannot see beta id
	rrX := httptest.NewRecorder()
	h.ServeHTTP(rrX, httptest.NewRequest(http.MethodGet, "/api/runs/alpha/"+betaRun.ID, nil))
	if rrX.Code != http.StatusNotFound {
		t.Fatalf("cross-repo get status %d want 404: %s", rrX.Code, rrX.Body)
	}

	// unknown repo
	rrU := httptest.NewRecorder()
	h.ServeHTTP(rrU, httptest.NewRequest(http.MethodGet, "/api/runs/missing", nil))
	if rrU.Code != http.StatusNotFound {
		t.Fatalf("unknown repo status %d want 404: %s", rrU.Code, rrU.Body)
	}

	// unknown run
	rrUR := httptest.NewRecorder()
	h.ServeHTTP(rrUR, httptest.NewRequest(http.MethodGet, "/api/runs/alpha/no-such-run", nil))
	if rrUR.Code != http.StatusNotFound {
		t.Fatalf("unknown run status %d want 404: %s", rrUR.Code, rrUR.Body)
	}

	// flat list/start gone
	rrFlat := httptest.NewRecorder()
	h.ServeHTTP(rrFlat, httptest.NewRequest(http.MethodGet, "/api/runs", nil))
	if rrFlat.Code == http.StatusOK {
		t.Fatalf("flat GET /api/runs still served")
	}
}
