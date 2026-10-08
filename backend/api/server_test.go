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

	deadline := time.Now().Add(5 * time.Second)
	for {
		rr2 := httptest.NewRecorder()
		h.ServeHTTP(rr2, httptest.NewRequest(http.MethodGet, "/api/runs/"+run.ID, nil))
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
	root := t.TempDir()
	project := filepath.Join(root, "proj")
	workflows := filepath.Join(root, "workflows")
	storage := filepath.Join(root, "storage")
	_ = os.MkdirAll(project, 0o755)
	_ = os.MkdirAll(workflows, 0o755)
	_ = os.WriteFile(filepath.Join(project, "slow.sh"), []byte("#!/bin/sh\necho hello\nsleep 0.1\necho world\n"), 0o755)
	_ = os.WriteFile(filepath.Join(workflows, "slow.yaml"), []byte(`
id: slow
steps:
  - id: s
    mode: series
    processes:
      - id: p
        command: ./slow.sh
`), 0o644)

	eng := engine.New(engine.Config{WorkflowsDir: workflows, StorageRoot: storage})
	srv := httptest.NewServer((&api.Server{Eng: eng}).Handler())
	defer srv.Close()

	body, _ := json.Marshal(map[string]string{"workflowId": "slow", "projectPath": project})
	res, err := http.Post(srv.URL+"/api/runs", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var run engine.WorkflowRun
	_ = json.NewDecoder(res.Body).Decode(&run)

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

	// Reconnect: same event shape from JSONL history.
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
