package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/justengland/subspace/backend/api"
	"github.com/justengland/subspace/backend/engine"
)

func TestHTTPStartAndGet(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "proj")
	workflows := filepath.Join(root, "workflows")
	storage := filepath.Join(root, "storage")
	_ = os.MkdirAll(project, 0o755)
	_ = os.MkdirAll(workflows, 0o755)
	_ = os.WriteFile(filepath.Join(project, "ok.sh"), []byte("#!/bin/sh\necho ok\n"), 0o755)
	_ = os.WriteFile(filepath.Join(workflows, "one.yaml"), []byte(`
id: one
steps:
  - id: s
    mode: series
    processes:
      - id: p
        command: ./ok.sh
`), 0o644)

	eng := engine.New(engine.Config{WorkflowsDir: workflows, StorageRoot: storage})
	h := (&api.Server{Eng: eng}).Handler()

	body, _ := json.Marshal(map[string]string{"workflowId": "one", "projectPath": project})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/runs", bytes.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("start status %d: %s", rr.Code, rr.Body)
	}
	var run engine.WorkflowRun
	if err := json.Unmarshal(rr.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}
	if run.Status != engine.StatusSucceeded {
		t.Fatalf("status=%s", run.Status)
	}

	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, httptest.NewRequest(http.MethodGet, "/api/runs/"+run.ID, nil))
	if rr2.Code != http.StatusOK {
		t.Fatalf("get status %d", rr2.Code)
	}
	var got engine.WorkflowRun
	_ = json.Unmarshal(rr2.Body.Bytes(), &got)
	if got.Status != engine.StatusSucceeded || got.ID != run.ID {
		t.Fatalf("get=%+v", got)
	}
}
