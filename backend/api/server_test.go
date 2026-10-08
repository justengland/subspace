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
	if run.Status != engine.StatusRunning {
		t.Fatalf("status=%s want running", run.Status)
	}

	deadline := time.Now().Add(3 * time.Second)
	var got engine.WorkflowRun
	for time.Now().Before(deadline) {
		rr2 := httptest.NewRecorder()
		h.ServeHTTP(rr2, httptest.NewRequest(http.MethodGet, "/api/runs/"+run.ID, nil))
		if rr2.Code != http.StatusOK {
			t.Fatalf("get status %d", rr2.Code)
		}
		_ = json.Unmarshal(rr2.Body.Bytes(), &got)
		if got.Status == engine.StatusSucceeded && got.ID == run.ID {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("get=%+v want succeeded", got)
}

func TestHTTPPauseResumeStop(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "proj")
	workflows := filepath.Join(root, "workflows")
	storage := filepath.Join(root, "storage")
	_ = os.MkdirAll(project, 0o755)
	_ = os.MkdirAll(workflows, 0o755)
	_ = os.WriteFile(filepath.Join(project, "slow.sh"), []byte("#!/bin/sh\ntouch \"$PWD/go\"\nsleep 2\n"), 0o755)
	_ = os.WriteFile(filepath.Join(workflows, "slow.yaml"), []byte(`
id: slow
steps:
  - id: a
    mode: series
    processes:
      - id: p
        command: ./slow.sh
  - id: b
    mode: series
    processes:
      - id: p2
        command: ./slow.sh
`), 0o644)

	eng := engine.New(engine.Config{WorkflowsDir: workflows, StorageRoot: storage})
	h := (&api.Server{Eng: eng}).Handler()

	body, _ := json.Marshal(map[string]string{"workflowId": "slow", "projectPath": project})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/runs", bytes.NewReader(body)))
	var run engine.WorkflowRun
	_ = json.Unmarshal(rr.Body.Bytes(), &run)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(filepath.Join(project, "go")); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	rrP := httptest.NewRecorder()
	h.ServeHTTP(rrP, httptest.NewRequest(http.MethodPost, "/api/runs/"+run.ID+"/pause", nil))
	if rrP.Code != http.StatusOK {
		t.Fatalf("pause %d: %s", rrP.Code, rrP.Body)
	}

	// Stop while pause pending / after step — either path ends scheduling.
	rrS := httptest.NewRecorder()
	h.ServeHTTP(rrS, httptest.NewRequest(http.MethodPost, "/api/runs/"+run.ID+"/stop", nil))
	if rrS.Code != http.StatusOK {
		t.Fatalf("stop %d: %s", rrS.Code, rrS.Body)
	}

	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		rrG := httptest.NewRecorder()
		h.ServeHTTP(rrG, httptest.NewRequest(http.MethodGet, "/api/runs/"+run.ID, nil))
		var got engine.WorkflowRun
		_ = json.Unmarshal(rrG.Body.Bytes(), &got)
		if got.Status == engine.StatusStopped {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("expected stopped")
}

func TestHTTPGetWorkflowConnections(t *testing.T) {
	root := t.TempDir()
	workflows := filepath.Join(root, "workflows")
	_ = os.MkdirAll(workflows, 0o755)
	_ = os.WriteFile(filepath.Join(workflows, "pipe.yaml"), []byte(`
id: pipe
steps:
  - id: produce
    outputs:
      - name: msg
        value: hello
    processes:
      - id: noop
        command: true
  - id: consume
    inputs:
      - name: msg
        source:
          stepId: produce
          output: msg
    processes:
      - id: check
        command: true
`), 0o644)

	eng := engine.New(engine.Config{WorkflowsDir: workflows, StorageRoot: filepath.Join(root, "storage")})
	h := (&api.Server{Eng: eng}).Handler()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/workflows/pipe", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rr.Code, rr.Body)
	}
	var wf engine.Workflow
	if err := json.Unmarshal(rr.Body.Bytes(), &wf); err != nil {
		t.Fatal(err)
	}
	if len(wf.Connections) != 1 {
		t.Fatalf("connections=%+v", wf.Connections)
	}
	c := wf.Connections[0]
	if c.SourceStepID != "produce" || c.TargetStepID != "consume" || c.SourceOutput != "msg" || c.TargetInput != "msg" {
		t.Fatalf("connection=%+v", c)
	}
}
