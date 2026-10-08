package engine_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/justengland/subspace/backend/engine"
)

func TestPauseHoldsThenResumeContinues(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "proj")
	workflows := filepath.Join(root, "workflows")
	storage := filepath.Join(root, "storage")
	mustMkdir(t, project)
	mustMkdir(t, workflows)

	writeFile(t, filepath.Join(project, "slow1.sh"), `#!/bin/sh
touch "$PWD/s1.started"
sleep 2
touch "$PWD/s1.done"
`)
	writeFile(t, filepath.Join(project, "step2.sh"), `#!/bin/sh
touch "$PWD/s2.done"
`)
	chmodX(t, filepath.Join(project, "slow1.sh"))
	chmodX(t, filepath.Join(project, "step2.sh"))

	writeFile(t, filepath.Join(workflows, "two-step.yaml"), `
id: two-step
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
`)

	eng := engine.New(engine.Config{WorkflowsDir: workflows, StorageRoot: storage})
	run, err := eng.Start(engine.StartRequest{WorkflowID: "two-step", ProjectPath: project})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if run.Status != engine.StatusRunning {
		t.Fatalf("Start status=%s want running", run.Status)
	}

	waitFile(t, filepath.Join(project, "s1.started"), 3*time.Second)
	if err := eng.Pause(run.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	waitStatus(t, eng, run.ID, engine.StatusPaused, 5*time.Second)
	if _, err := os.Stat(filepath.Join(project, "s1.done")); err != nil {
		t.Fatalf("Pause should let in-flight ProcessRun finish: %v", err)
	}
	if _, err := os.Stat(filepath.Join(project, "s2.done")); err == nil {
		t.Fatal("step two ran before Resume")
	}

	if err := eng.Resume(run.ID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	waitStatus(t, eng, run.ID, engine.StatusSucceeded, 5*time.Second)
	if _, err := os.Stat(filepath.Join(project, "s2.done")); err != nil {
		t.Fatalf("step two did not run after Resume: %v", err)
	}
}

func TestStopSIGTERMsAndEndsScheduling(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "proj")
	workflows := filepath.Join(root, "workflows")
	storage := filepath.Join(root, "storage")
	mustMkdir(t, project)
	mustMkdir(t, workflows)

	writeFile(t, filepath.Join(project, "trap.sh"), `#!/bin/sh
trap 'touch "$PWD/termed"; exit 0' TERM
touch "$PWD/started"
sleep 30
touch "$PWD/should-not"
`)
	writeFile(t, filepath.Join(project, "next.sh"), `#!/bin/sh
touch "$PWD/next.done"
`)
	chmodX(t, filepath.Join(project, "trap.sh"))
	chmodX(t, filepath.Join(project, "next.sh"))

	writeFile(t, filepath.Join(workflows, "stop-me.yaml"), `
id: stop-me
steps:
  - id: one
    mode: series
    processes:
      - id: long
        command: ./trap.sh
  - id: two
    mode: series
    processes:
      - id: next
        command: ./next.sh
`)

	eng := engine.New(engine.Config{WorkflowsDir: workflows, StorageRoot: storage})
	run, err := eng.Start(engine.StartRequest{WorkflowID: "stop-me", ProjectPath: project})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFile(t, filepath.Join(project, "started"), 3*time.Second)
	if err := eng.Stop(run.ID); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	waitStatus(t, eng, run.ID, engine.StatusStopped, 5*time.Second)
	waitFile(t, filepath.Join(project, "termed"), 2*time.Second)
	if _, err := os.Stat(filepath.Join(project, "should-not")); err == nil {
		t.Fatal("process continued after SIGTERM")
	}
	if _, err := os.Stat(filepath.Join(project, "next.done")); err == nil {
		t.Fatal("next Step scheduled after Stop")
	}
}

func TestSeriesWorkflowSucceeds(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "my-project")
	workflows := filepath.Join(root, "workflows")
	storage := filepath.Join(root, "run-storage")
	mustMkdir(t, project)
	mustMkdir(t, workflows)

	// Project scripts: series must run a then b (marker proves order).
	writeFile(t, filepath.Join(project, "a.sh"), "#!/bin/sh\necho a-out\ntouch \"$PWD/a.done\"\n")
	writeFile(t, filepath.Join(project, "b.sh"), "#!/bin/sh\ntest -f \"$PWD/a.done\" || exit 1\necho b-out\ntouch \"$PWD/b.done\"\n")
	chmodX(t, filepath.Join(project, "a.sh"))
	chmodX(t, filepath.Join(project, "b.sh"))

	writeFile(t, filepath.Join(workflows, "series-hello.yaml"), `
id: series-hello
name: Series Hello
steps:
  - id: greet
    name: Greet
    mode: series
    processes:
      - id: run-a
        name: Run A
        command: ./a.sh
      - id: run-b
        name: Run B
        command: ./b.sh
`)

	eng := engine.New(engine.Config{
		WorkflowsDir: workflows,
		StorageRoot:  storage,
	})

	run, err := eng.Start(engine.StartRequest{
		WorkflowID:  "series-hello",
		ProjectPath: project,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if run.Status != engine.StatusRunning {
		t.Fatalf("status=%s want running", run.Status)
	}
	waitStatus(t, eng, run.ID, engine.StatusSucceeded, 5*time.Second)

	got, err := eng.Get(run.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != engine.StatusSucceeded {
		t.Fatalf("Get status=%s", got.Status)
	}

	if _, err := os.Stat(filepath.Join(project, "a.done")); err != nil {
		t.Fatalf("a.sh did not run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(project, "b.done")); err != nil {
		t.Fatalf("b.sh did not run (or ran before a): %v", err)
	}

	runDir := filepath.Join(storage, "my-project", run.ID)
	jsonlPath := filepath.Join(runDir, "events.jsonl")
	data, err := os.ReadFile(jsonlPath)
	if err != nil {
		t.Fatalf("read JSONL: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) < 2 {
		t.Fatalf("JSONL too short: %q", data)
	}
	var types []string
	for _, line := range lines {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("bad JSONL line %q: %v", line, err)
		}
		typ, _ := ev["type"].(string)
		types = append(types, typ)
	}
	joined := strings.Join(types, ",")
	for _, want := range []string{"workflow_started", "process_started", "stdout", "workflow_succeeded"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("JSONL missing %q in %v", want, types)
		}
	}
	if !strings.Contains(string(data), "a-out") || !strings.Contains(string(data), "b-out") {
		t.Fatalf("JSONL missing stdout chunks: %s", data)
	}
}

func TestInputSourceResolvesAcrossSteps(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "proj")
	workflows := filepath.Join(root, "workflows")
	storage := filepath.Join(root, "storage")
	mustMkdir(t, project)
	mustMkdir(t, workflows)

	writeFile(t, filepath.Join(project, "check.sh"), `#!/bin/sh
test "$SUBSPACE_INPUT_msg" = "hello-from-upstream" || { echo "got=$SUBSPACE_INPUT_msg" >&2; exit 1; }
echo ok
`)
	chmodX(t, filepath.Join(project, "check.sh"))

	writeFile(t, filepath.Join(workflows, "pipe.yaml"), `
id: pipe
steps:
  - id: produce
    mode: series
    outputs:
      - name: msg
        value: hello-from-upstream
    processes:
      - id: noop
        command: true
  - id: consume
    mode: series
    inputs:
      - name: msg
        source:
          stepId: produce
          output: msg
    processes:
      - id: check
        command: ./check.sh
`)

	eng := engine.New(engine.Config{WorkflowsDir: workflows, StorageRoot: storage})
	run, err := eng.Start(engine.StartRequest{WorkflowID: "pipe", ProjectPath: project})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitStatus(t, eng, run.ID, engine.StatusSucceeded, 5*time.Second)
}

func TestConnectionsDerivedFromInputSource(t *testing.T) {
	root := t.TempDir()
	workflows := filepath.Join(root, "workflows")
	mustMkdir(t, workflows)

	writeFile(t, filepath.Join(workflows, "pipe.yaml"), `
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
`)

	eng := engine.New(engine.Config{WorkflowsDir: workflows, StorageRoot: filepath.Join(root, "storage")})
	wf, err := eng.GetWorkflow("pipe")
	if err != nil {
		t.Fatalf("GetWorkflow: %v", err)
	}
	if len(wf.Connections) != 1 {
		t.Fatalf("connections=%d want 1: %+v", len(wf.Connections), wf.Connections)
	}
	c := wf.Connections[0]
	if c.SourceStepID != "produce" || c.SourceOutput != "msg" || c.TargetStepID != "consume" || c.TargetInput != "msg" {
		t.Fatalf("connection=%+v", c)
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

func waitStatus(t *testing.T, eng *engine.Engine, id, want string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	var last string
	for time.Now().Before(deadline) {
		got, err := eng.Get(id)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		last = got.Status
		if got.Status == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting status=%s got=%s", want, last)
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, p, body string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(strings.TrimSpace(body)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func chmodX(t *testing.T, p string) {
	t.Helper()
	if err := os.Chmod(p, 0o755); err != nil {
		t.Fatal(err)
	}
}
