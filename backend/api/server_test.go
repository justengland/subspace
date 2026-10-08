package api_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/justengland/subspace/backend/api"
	"github.com/justengland/subspace/backend/engine"
	"github.com/justengland/subspace/backend/registry"
)

func setupRepo(t *testing.T, home, name string) string {
	t.Helper()
	project := filepath.Join(home, "proj-"+name)
	mustMkdir(t, project)
	if err := registry.Add(home, name, project); err != nil {
		t.Fatal(err)
	}
	return project
}

func TestHTTPStartAndGet(t *testing.T) {
	home := t.TempDir()
	project := setupRepo(t, home, "demo")
	_ = os.WriteFile(filepath.Join(project, "ok.sh"), []byte("#!/bin/sh\necho ok\n"), 0o755)
	writeWorkflow(t, home, "demo", "one", `
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
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/runs/demo", bytes.NewReader(body)))
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

	deadline := time.Now().Add(5 * time.Second)
	for {
		rr2 := httptest.NewRecorder()
		h.ServeHTTP(rr2, httptest.NewRequest(http.MethodGet, "/api/runs/demo/"+run.ID, nil))
		if rr2.Code != http.StatusOK {
			t.Fatalf("get status %d", rr2.Code)
		}
		var got engine.WorkflowRun
		_ = json.Unmarshal(rr2.Body.Bytes(), &got)
		if got.Status == engine.StatusSucceeded {
			break
		}
		if got.Status != engine.StatusRunning {
			t.Fatalf("get=%+v", got)
		}
		if time.Now().After(deadline) {
			t.Fatal("timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestWebSocketTimeline(t *testing.T) {
	home := t.TempDir()
	project := setupRepo(t, home, "demo")
	_ = os.WriteFile(filepath.Join(project, "slow.sh"), []byte("#!/bin/sh\necho hello\nsleep 0.1\necho world\n"), 0o755)
	writeWorkflow(t, home, "demo", "slow", `
id: slow
steps:
  - id: s
    mode: series
    processes:
      - id: p
        command: ./slow.sh
`)

	eng := engine.New(engine.Config{StorageRoot: home})
	srv := httptest.NewServer((&api.Server{Eng: eng, Home: home}).Handler())
	defer srv.Close()

	body, _ := json.Marshal(map[string]string{"workflowId": "slow"})
	res, err := http.Post(srv.URL+"/api/runs/demo", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var run engine.WorkflowRun
	_ = json.NewDecoder(res.Body).Decode(&run)

	// ponytail: events stay flat until #20
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/runs/" + run.ID + "/events"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	var types []string
	var stdout []string
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		var ev map[string]any
		if err := conn.ReadJSON(&ev); err != nil {
			break
		}
		typ, _ := ev["type"].(string)
		types = append(types, typ)
		if typ == "stdout" {
			stdout = append(stdout, ev["data"].(string))
		}
		if typ == "workflow_succeeded" || typ == "workflow_failed" {
			break
		}
	}
	joined := strings.Join(types, ",")
	for _, want := range []string{"workflow_started", "stdout", "workflow_succeeded"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %v", want, types)
		}
	}
	if len(stdout) < 2 {
		t.Fatalf("stdout=%v", stdout)
	}

	conn2, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn2.Close()
	_ = conn2.SetReadDeadline(time.Now().Add(2 * time.Second))
	var again []string
	for {
		var ev map[string]any
		if err := conn2.ReadJSON(&ev); err != nil {
			break
		}
		again = append(again, ev["type"].(string))
	}
	if !strings.Contains(strings.Join(again, ","), "stdout") {
		t.Fatalf("history=%v", again)
	}
}

func TestHTTPParallelFailShowsProcessRuns(t *testing.T) {
	home := t.TempDir()
	project := setupRepo(t, home, "demo")
	_ = os.WriteFile(filepath.Join(project, "long.sh"), []byte("#!/bin/sh\ntrap 'exit 143' TERM\nsleep 30\n"), 0o755)
	_ = os.WriteFile(filepath.Join(project, "fail.sh"), []byte("#!/bin/sh\nsleep 0.2\nexit 1\n"), 0o755)
	writeWorkflow(t, home, "demo", "pf", `
id: pf
steps:
  - id: s
    mode: parallel
    processes:
      - id: long
        command: ./long.sh
      - id: fail
        command: ./fail.sh
`)

	eng := engine.New(engine.Config{StorageRoot: home})
	h := (&api.Server{Eng: eng, Home: home}).Handler()

	body, _ := json.Marshal(map[string]string{"workflowId": "pf"})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/runs/demo", bytes.NewReader(body)))
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

	deadline := time.Now().Add(5 * time.Second)
	for {
		rr2 := httptest.NewRecorder()
		h.ServeHTTP(rr2, httptest.NewRequest(http.MethodGet, "/api/runs/demo/"+run.ID, nil))
		if rr2.Code != http.StatusOK {
			t.Fatalf("get status %d", rr2.Code)
		}
		_ = json.Unmarshal(rr2.Body.Bytes(), &run)
		if run.Status == engine.StatusFailed {
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
	if len(run.ProcessRuns) < 2 {
		t.Fatalf("processRuns=%d", len(run.ProcessRuns))
	}
	foundFail := false
	for _, pr := range run.ProcessRuns {
		if pr.ProcessID == "fail" && pr.Status == engine.StatusFailed {
			foundFail = true
		}
	}
	if !foundFail {
		t.Fatalf("missing failed ProcessRun: %+v", run.ProcessRuns)
	}
}

func TestHTTPGetWorkflowConnections(t *testing.T) {
	home := t.TempDir()
	proj := filepath.Join(home, "proj")
	_ = os.MkdirAll(proj, 0o755)
	if err := registry.Add(home, "demo", proj); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(home, "demo", "workflows")
	_ = os.MkdirAll(dir, 0o755)
	_ = os.WriteFile(filepath.Join(dir, "pipe.yaml"), []byte(`
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

	eng := engine.New(engine.Config{StorageRoot: home})
	h := (&api.Server{Eng: eng, Home: home}).Handler()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/workflows/demo/pipe", nil))
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
