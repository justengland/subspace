package engine_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/justengland/subspace/backend/engine"
)

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
	if run.Status != engine.StatusSucceeded {
		t.Fatalf("status=%s want succeeded", run.Status)
	}

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

	// Downstream process must see resolved Input via env SUBSPACE_INPUT_<name>.
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
	if run.Status != engine.StatusSucceeded {
		t.Fatalf("status=%s want succeeded", run.Status)
	}
}

func TestConnectionsDerivedFromInputSource(t *testing.T) {
	root := t.TempDir()
	workflows := filepath.Join(root, "workflows")
	mustMkdir(t, workflows)

	// No connections[] in YAML — derived on read from Input.source only.
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

func TestParallelWorkflowSucceeds(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "my-project")
	workflows := filepath.Join(root, "workflows")
	storage := filepath.Join(root, "run-storage")
	mustMkdir(t, project)
	mustMkdir(t, workflows)

	writeFile(t, filepath.Join(project, "p1.sh"), "#!/bin/sh\necho p1-out\ntouch \"$PWD/p1.done\"\n")
	writeFile(t, filepath.Join(project, "p2.sh"), "#!/bin/sh\necho p2-out\ntouch \"$PWD/p2.done\"\n")
	chmodX(t, filepath.Join(project, "p1.sh"))
	chmodX(t, filepath.Join(project, "p2.sh"))

	writeFile(t, filepath.Join(workflows, "parallel-hello.yaml"), `
id: parallel-hello
name: Parallel Hello
steps:
  - id: both
    name: Both
    mode: parallel
    processes:
      - id: run-p1
        name: Run P1
        command: ./p1.sh
      - id: run-p2
        name: Run P2
        command: ./p2.sh
`)

	eng := engine.New(engine.Config{WorkflowsDir: workflows, StorageRoot: storage})
	run, err := eng.Start(engine.StartRequest{WorkflowID: "parallel-hello", ProjectPath: project})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if run.Status != engine.StatusSucceeded {
		t.Fatalf("status=%s want succeeded", run.Status)
	}
	if _, err := os.Stat(filepath.Join(project, "p1.done")); err != nil {
		t.Fatalf("p1 missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(project, "p2.done")); err != nil {
		t.Fatalf("p2 missing: %v", err)
	}
	if len(run.ProcessRuns) != 2 {
		t.Fatalf("ProcessRuns=%d want 2", len(run.ProcessRuns))
	}
	for _, pr := range run.ProcessRuns {
		if pr.Status != engine.StatusSucceeded {
			t.Fatalf("process %s status=%s", pr.ProcessID, pr.Status)
		}
	}
}

func TestParallelFailFastSIGTERMsSiblings(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "my-project")
	workflows := filepath.Join(root, "workflows")
	storage := filepath.Join(root, "run-storage")
	mustMkdir(t, project)
	mustMkdir(t, workflows)

	writeFile(t, filepath.Join(project, "long.sh"), `#!/bin/sh
trap 'touch "$PWD/long.term"; exit 143' TERM
touch "$PWD/long.started"
sleep 30
touch "$PWD/long.finished"
`)
	writeFile(t, filepath.Join(project, "fail.sh"), "#!/bin/sh\nsleep 0.2\nexit 1\n")
	chmodX(t, filepath.Join(project, "long.sh"))
	chmodX(t, filepath.Join(project, "fail.sh"))

	writeFile(t, filepath.Join(workflows, "parallel-fail.yaml"), `
id: parallel-fail
name: Parallel Fail
steps:
  - id: both
    mode: parallel
    processes:
      - id: long
        command: ./long.sh
      - id: fail
        command: ./fail.sh
`)

	eng := engine.New(engine.Config{WorkflowsDir: workflows, StorageRoot: storage})
	run, err := eng.Start(engine.StartRequest{WorkflowID: "parallel-fail", ProjectPath: project})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if run.Status != engine.StatusFailed {
		t.Fatalf("status=%s want failed", run.Status)
	}
	if _, err := os.Stat(filepath.Join(project, "long.started")); err != nil {
		t.Fatalf("long never started: %v", err)
	}
	if _, err := os.Stat(filepath.Join(project, "long.finished")); err == nil {
		t.Fatal("long finished; sibling was not stopped")
	}
	if _, err := os.Stat(filepath.Join(project, "long.term")); err != nil {
		t.Fatalf("long did not receive SIGTERM: %v", err)
	}

	byID := map[string]engine.ProcessRun{}
	for _, pr := range run.ProcessRuns {
		byID[pr.ProcessID] = pr
	}
	if byID["fail"].Status != engine.StatusFailed {
		t.Fatalf("fail ProcessRun status=%s", byID["fail"].Status)
	}
	if byID["long"].Status != engine.StatusStopped {
		t.Fatalf("long ProcessRun status=%s want stopped", byID["long"].Status)
	}

	got, err := eng.Get(run.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != engine.StatusFailed {
		t.Fatalf("Get status=%s", got.Status)
	}
	if len(got.ProcessRuns) < 2 {
		t.Fatalf("Get ProcessRuns=%d", len(got.ProcessRuns))
	}
}
