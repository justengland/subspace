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

func TestHTTPNestedPauseResumeStopRewind(t *testing.T) {
	home := t.TempDir()
	project := setupRepo(t, home, "demo")
	_ = os.WriteFile(filepath.Join(project, "slow1.sh"), []byte(`#!/bin/sh
touch "$PWD/s1.started"
sleep 2
touch "$PWD/s1.done"
`), 0o755)
	_ = os.WriteFile(filepath.Join(project, "step2.sh"), []byte(`#!/bin/sh
touch "$PWD/s2.done"
`), 0o755)
	_ = os.WriteFile(filepath.Join(project, "step3.sh"), []byte(`#!/bin/sh
touch "$PWD/s3.done"
`), 0o755)
	writeWorkflow(t, home, "demo", "three", `
id: three
steps:
  - id: one
    mode: series
    processes:
      - id: p1
        command: ./slow1.sh
  - id: two
    mode: series
    processes:
      - id: p2
        command: ./step2.sh
  - id: three
    mode: series
    processes:
      - id: p3
        command: ./step3.sh
`)

	eng := engine.New(engine.Config{StorageRoot: home})
	h := (&api.Server{Eng: eng, Home: home}).Handler()

	body, _ := json.Marshal(map[string]string{"workflowId": "three"})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/runs/demo", bytes.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("start status %d: %s", rr.Code, rr.Body)
	}
	var run engine.WorkflowRun
	if err := json.Unmarshal(rr.Body.Bytes(), &run); err != nil {
		t.Fatal(err)
	}

	waitFile(t, filepath.Join(project, "s1.started"), 3*time.Second)

	rrP := httptest.NewRecorder()
	h.ServeHTTP(rrP, httptest.NewRequest(http.MethodPost, "/api/runs/demo/"+run.ID+"/pause", nil))
	if rrP.Code != http.StatusOK {
		t.Fatalf("pause status %d: %s", rrP.Code, rrP.Body)
	}
	waitHTTPStatus(t, h, "/api/runs/demo/"+run.ID, engine.StatusPaused)

	if _, err := os.Stat(filepath.Join(project, "s2.done")); err == nil {
		t.Fatal("step two ran before Resume")
	}

	// rewind while paused (discard step two if any; cursor at one)
	rrW := httptest.NewRecorder()
	rewindBody, _ := json.Marshal(map[string]string{"stepId": "one"})
	h.ServeHTTP(rrW, httptest.NewRequest(http.MethodPost, "/api/runs/demo/"+run.ID+"/rewind", bytes.NewReader(rewindBody)))
	if rrW.Code != http.StatusOK {
		t.Fatalf("rewind status %d: %s", rrW.Code, rrW.Body)
	}

	rrR := httptest.NewRecorder()
	h.ServeHTTP(rrR, httptest.NewRequest(http.MethodPost, "/api/runs/demo/"+run.ID+"/resume", nil))
	if rrR.Code != http.StatusOK {
		t.Fatalf("resume status %d: %s", rrR.Code, rrR.Body)
	}
	waitHTTPStatus(t, h, "/api/runs/demo/"+run.ID, engine.StatusSucceeded)
	if _, err := os.Stat(filepath.Join(project, "s3.done")); err != nil {
		t.Fatalf("step three missing after resume: %v", err)
	}

	// flat control routes gone
	for _, path := range []string{
		"/api/runs/" + run.ID + "/pause",
		"/api/runs/" + run.ID + "/resume",
		"/api/runs/" + run.ID + "/stop",
		"/api/runs/" + run.ID + "/rewind",
	} {
		rrF := httptest.NewRecorder()
		h.ServeHTTP(rrF, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(rewindBody)))
		if rrF.Code == http.StatusOK {
			t.Fatalf("flat %s still served", path)
		}
	}
}

func TestHTTPNestedStop(t *testing.T) {
	home := t.TempDir()
	project := setupRepo(t, home, "demo")
	_ = os.WriteFile(filepath.Join(project, "trap.sh"), []byte(`#!/bin/sh
trap 'touch "$PWD/termed"; exit 0' TERM
touch "$PWD/started"
sleep 30
`), 0o755)
	writeWorkflow(t, home, "demo", "stop-me", `
id: stop-me
steps:
  - id: one
    mode: series
    processes:
      - id: p
        command: ./trap.sh
`)

	eng := engine.New(engine.Config{StorageRoot: home})
	h := (&api.Server{Eng: eng, Home: home}).Handler()

	body, _ := json.Marshal(map[string]string{"workflowId": "stop-me"})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/runs/demo", bytes.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("start %d: %s", rr.Code, rr.Body)
	}
	var run engine.WorkflowRun
	_ = json.Unmarshal(rr.Body.Bytes(), &run)
	waitFile(t, filepath.Join(project, "started"), 3*time.Second)

	rrS := httptest.NewRecorder()
	h.ServeHTTP(rrS, httptest.NewRequest(http.MethodPost, "/api/runs/demo/"+run.ID+"/stop", nil))
	if rrS.Code != http.StatusOK {
		t.Fatalf("stop status %d: %s", rrS.Code, rrS.Body)
	}
	waitHTTPStatus(t, h, "/api/runs/demo/"+run.ID, engine.StatusStopped)
}

func TestWebSocketNestedTimeline(t *testing.T) {
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

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/runs/demo/" + run.ID + "/events"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	var types []string
	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		var ev map[string]any
		if err := conn.ReadJSON(&ev); err != nil {
			break
		}
		typ, _ := ev["type"].(string)
		types = append(types, typ)
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

	// flat events gone
	flatURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/runs/" + run.ID + "/events"
	if _, _, err := websocket.DefaultDialer.Dial(flatURL, nil); err == nil {
		t.Fatal("flat /api/runs/{id}/events still served")
	}
}

func waitFile(t *testing.T, path string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", path)
}

func waitHTTPStatus(t *testing.T, h http.Handler, path, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusOK {
			t.Fatalf("get %s status %d: %s", path, rr.Code, rr.Body)
		}
		var run engine.WorkflowRun
		_ = json.Unmarshal(rr.Body.Bytes(), &run)
		if run.Status == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s got %s", want, run.Status)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
