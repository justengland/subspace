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

// Cold run under alpha with the same id as a live beta run must not pause beta.
func TestHTTPControlHonorsRepoNotJustRunID(t *testing.T) {
	home := t.TempDir()
	_ = setupRepo(t, home, "alpha")
	projB := setupRepo(t, home, "beta")
	_ = os.WriteFile(filepath.Join(projB, "slow.sh"), []byte(`#!/bin/sh
touch "$PWD/started"
sleep 30
`), 0o755)
	writeWorkflow(t, home, "beta", "slow", `
id: slow
steps:
  - id: one
    mode: series
    processes:
      - id: p
        command: ./slow.sh
`)

	eng := engine.New(engine.Config{StorageRoot: home})
	h := (&api.Server{Eng: eng, Home: home}).Handler()

	body, _ := json.Marshal(map[string]string{"workflowId": "slow"})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/runs/beta", bytes.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("start %d: %s", rr.Code, rr.Body)
	}
	var run engine.WorkflowRun
	_ = json.Unmarshal(rr.Body.Bytes(), &run)
	waitFile(t, filepath.Join(projB, "started"), 3*time.Second)

	alphaDir := filepath.Join(home, "alpha", run.ID)
	if err := os.MkdirAll(alphaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cold := `{"type":"workflow_started","workflowId":"other","runId":"` + run.ID + `","projectPath":"/tmp/alpha"}` + "\n" +
		`{"type":"workflow_succeeded"}` + "\n"
	if err := os.WriteFile(filepath.Join(alphaDir, "events.jsonl"), []byte(cold), 0o644); err != nil {
		t.Fatal(err)
	}

	rrP := httptest.NewRecorder()
	h.ServeHTTP(rrP, httptest.NewRequest(http.MethodPost, "/api/runs/alpha/"+run.ID+"/pause", nil))
	if rrP.Code == http.StatusOK {
		t.Fatalf("pause on alpha cold run succeeded; must not control by runId alone")
	}

	rrB := httptest.NewRecorder()
	h.ServeHTTP(rrB, httptest.NewRequest(http.MethodGet, "/api/runs/beta/"+run.ID, nil))
	var beta engine.WorkflowRun
	_ = json.Unmarshal(rrB.Body.Bytes(), &beta)
	if beta.Status == engine.StatusPaused {
		t.Fatal("beta live run was paused via alpha path (repo isolation broken)")
	}
	if beta.Status != engine.StatusRunning {
		t.Fatalf("beta status=%s want running", beta.Status)
	}

	// GET cold alpha must still resolve (not shadowed by beta live).
	rrA := httptest.NewRecorder()
	h.ServeHTTP(rrA, httptest.NewRequest(http.MethodGet, "/api/runs/alpha/"+run.ID, nil))
	if rrA.Code != http.StatusOK {
		t.Fatalf("get alpha cold %d: %s", rrA.Code, rrA.Body)
	}
	var alpha engine.WorkflowRun
	_ = json.Unmarshal(rrA.Body.Bytes(), &alpha)
	if alpha.WorkflowID != "other" || alpha.Status != engine.StatusSucceeded {
		t.Fatalf("alpha cold=%+v", alpha)
	}
}

func TestHTTPFollowHonorsRepoOnDisk(t *testing.T) {
	home := t.TempDir()
	_ = setupRepo(t, home, "alpha")
	_ = setupRepo(t, home, "beta")
	id := "20260101-000000-abcd"

	for _, repo := range []string{"alpha", "beta"} {
		dir := filepath.Join(home, repo, id)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		ev := `{"type":"workflow_started","workflowId":"` + repo + `-wf","runId":"` + id + `","projectPath":"/tmp/` + repo + `"}` + "\n" +
			`{"type":"workflow_succeeded"}` + "\n"
		if err := os.WriteFile(filepath.Join(dir, "events.jsonl"), []byte(ev), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	eng := engine.New(engine.Config{StorageRoot: home})
	srv := httptest.NewServer((&api.Server{Eng: eng, Home: home}).Handler())
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/runs/beta/" + id + "/events"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	var wfIDs []string
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		var ev map[string]any
		if err := conn.ReadJSON(&ev); err != nil {
			break
		}
		if ev["type"] == "workflow_started" {
			wfIDs = append(wfIDs, ev["workflowId"].(string))
		}
	}
	if len(wfIDs) != 1 || wfIDs[0] != "beta-wf" {
		t.Fatalf("Follow under beta got workflowIds=%v want [beta-wf]", wfIDs)
	}
}
