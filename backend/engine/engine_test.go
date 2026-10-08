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
	waitFile(t, filepath.Join(project, "s1.started"), 3*time.Second)
	if err := eng.Pause(run.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	_ = waitStatus(t, eng, run.ID, engine.StatusPaused)
	if _, err := os.Stat(filepath.Join(project, "s1.done")); err != nil {
		t.Fatalf("Pause should let in-flight ProcessRun finish: %v", err)
	}
	if _, err := os.Stat(filepath.Join(project, "s2.done")); err == nil {
		t.Fatal("step two ran before Resume")
	}
	if err := eng.Resume(run.ID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	_ = waitStatus(t, eng, run.ID, engine.StatusSucceeded)
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
	_ = waitStatus(t, eng, run.ID, engine.StatusStopped)
	waitFile(t, filepath.Join(project, "termed"), 2*time.Second)
	if _, err := os.Stat(filepath.Join(project, "should-not")); err == nil {
		t.Fatal("process continued after SIGTERM")
	}
	if _, err := os.Stat(filepath.Join(project, "next.done")); err == nil {
		t.Fatal("next Step scheduled after Stop")
	}
}

func TestRewindDiscardsLaterStepRunsThenResumeReexecutes(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "proj")
	workflows := filepath.Join(root, "workflows")
	storage := filepath.Join(root, "storage")
	mustMkdir(t, project)
	mustMkdir(t, workflows)

	writeFile(t, filepath.Join(project, "one.sh"), `#!/bin/sh
n=0
[ -f "$PWD/one.count" ] && n=$(cat "$PWD/one.count")
echo $((n+1)) > "$PWD/one.count"
touch "$PWD/one.done"
`)
	writeFile(t, filepath.Join(project, "two.sh"), `#!/bin/sh
touch "$PWD/two.started"
sleep 2
touch "$PWD/two.done"
`)
	writeFile(t, filepath.Join(project, "three.sh"), `#!/bin/sh
touch "$PWD/three.done"
`)
	chmodX(t, filepath.Join(project, "one.sh"))
	chmodX(t, filepath.Join(project, "two.sh"))
	chmodX(t, filepath.Join(project, "three.sh"))

	writeFile(t, filepath.Join(workflows, "three-step.yaml"), `
id: three-step
steps:
  - id: one
    mode: series
    processes:
      - id: p1
        command: ./one.sh
  - id: two
    mode: series
    processes:
      - id: p2
        command: ./two.sh
  - id: three
    mode: series
    processes:
      - id: p3
        command: ./three.sh
`)

	eng := engine.New(engine.Config{WorkflowsDir: workflows, StorageRoot: storage})
	run, err := eng.Start(engine.StartRequest{WorkflowID: "three-step", ProjectPath: project})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	waitFile(t, filepath.Join(project, "two.started"), 3*time.Second)
	if err := eng.Pause(run.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	got := waitStatus(t, eng, run.ID, engine.StatusPaused)
	if _, err := os.Stat(filepath.Join(project, "two.done")); err != nil {
		t.Fatalf("expected step two finished before hold: %v", err)
	}
	if _, err := os.Stat(filepath.Join(project, "three.done")); err == nil {
		t.Fatal("step three ran before Rewind")
	}
	if len(got.StepRuns) < 2 {
		t.Fatalf("StepRuns before Rewind=%d want >=2: %+v", len(got.StepRuns), got.StepRuns)
	}

	if err := eng.Rewind(run.ID, "one"); err != nil {
		t.Fatalf("Rewind: %v", err)
	}
	got, err = eng.Get(run.ID)
	if err != nil {
		t.Fatalf("Get after Rewind: %v", err)
	}
	if got.CursorStepID != "one" {
		t.Fatalf("cursor=%q want one", got.CursorStepID)
	}
	if len(got.StepRuns) != 0 {
		t.Fatalf("StepRuns after Rewind=%+v want discarded", got.StepRuns)
	}
	for _, pr := range got.ProcessRuns {
		if pr.StepID == "one" || pr.StepID == "two" || pr.StepID == "three" {
			t.Fatalf("ProcessRun still present after Rewind: %+v", pr)
		}
	}
	if _, err := os.Stat(filepath.Join(project, "three.done")); err == nil {
		t.Fatal("step three ran during Rewind")
	}

	_ = os.Remove(filepath.Join(project, "one.done"))
	_ = os.Remove(filepath.Join(project, "two.done"))
	_ = os.Remove(filepath.Join(project, "two.started"))

	if err := eng.Resume(run.ID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	_ = waitStatus(t, eng, run.ID, engine.StatusSucceeded)
	count := strings.TrimSpace(readFile(t, filepath.Join(project, "one.count")))
	if count != "2" {
		t.Fatalf("one.count=%q want 2 (re-executed after Rewind)", count)
	}
	if _, err := os.Stat(filepath.Join(project, "three.done")); err != nil {
		t.Fatalf("step three did not run after Resume: %v", err)
	}
}

func TestSeriesWorkflowSucceeds(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "my-project")
	workflows := filepath.Join(root, "workflows")
	storage := filepath.Join(root, "run-storage")
	mustMkdir(t, project)
	mustMkdir(t, workflows)

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

	_ = waitStatus(t, eng, run.ID, engine.StatusSucceeded)

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
	if run.Status != engine.StatusRunning {
		t.Fatalf("status=%s want running", run.Status)
	}
	_ = waitStatus(t, eng, run.ID, engine.StatusSucceeded)
}

func TestDecisionExactlyOneMatchRunsProcess(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "proj")
	workflows := filepath.Join(root, "workflows")
	storage := filepath.Join(root, "storage")
	mustMkdir(t, project)
	mustMkdir(t, workflows)

	writeFile(t, filepath.Join(project, "csv.sh"), "#!/bin/sh\ntouch \"$PWD/csv.done\"\n")
	writeFile(t, filepath.Join(project, "json.sh"), "#!/bin/sh\ntouch \"$PWD/json.done\"\n")
	chmodX(t, filepath.Join(project, "csv.sh"))
	chmodX(t, filepath.Join(project, "json.sh"))

	writeFile(t, filepath.Join(workflows, "decision.yaml"), `
id: decision
steps:
  - id: set-kind
    mode: series
    outputs:
      - name: kind
        value: csv
    processes:
      - id: noop
        command: true
  - id: branch
    mode: decision
    inputs:
      - name: kind
        source:
          stepId: set-kind
          output: kind
    processes:
      - id: handle-csv
        when:
          eq:
            input: kind
            value: csv
        command: ./csv.sh
      - id: handle-json
        when:
          in:
            input: kind
            values: [json, ndjson]
        command: ./json.sh
`)

	eng := engine.New(engine.Config{WorkflowsDir: workflows, StorageRoot: storage})
	run, err := eng.Start(engine.StartRequest{WorkflowID: "decision", ProjectPath: project})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	_ = waitStatus(t, eng, run.ID, engine.StatusSucceeded)
	if _, err := os.Stat(filepath.Join(project, "csv.done")); err != nil {
		t.Fatalf("csv process did not run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(project, "json.done")); err == nil {
		t.Fatal("json process ran but should not")
	}
}

func TestDecisionZeroMatchesFails(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "proj")
	workflows := filepath.Join(root, "workflows")
	storage := filepath.Join(root, "storage")
	mustMkdir(t, project)
	mustMkdir(t, workflows)

	writeFile(t, filepath.Join(project, "csv.sh"), "#!/bin/sh\ntouch \"$PWD/csv.done\"\n")
	chmodX(t, filepath.Join(project, "csv.sh"))

	writeFile(t, filepath.Join(workflows, "decision.yaml"), `
id: decision
steps:
  - id: set-kind
    mode: series
    outputs:
      - name: kind
        value: xml
    processes:
      - id: noop
        command: true
  - id: branch
    mode: decision
    inputs:
      - name: kind
        source:
          stepId: set-kind
          output: kind
    processes:
      - id: handle-csv
        when:
          eq:
            input: kind
            value: csv
        command: ./csv.sh
`)

	eng := engine.New(engine.Config{WorkflowsDir: workflows, StorageRoot: storage})
	run, err := eng.Start(engine.StartRequest{WorkflowID: "decision", ProjectPath: project})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	_ = waitStatus(t, eng, run.ID, engine.StatusFailed)
	if _, err := os.Stat(filepath.Join(project, "csv.done")); err == nil {
		t.Fatal("csv process ran on zero-match")
	}
}

func TestDecisionMultipleMatchesFails(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "proj")
	workflows := filepath.Join(root, "workflows")
	storage := filepath.Join(root, "storage")
	mustMkdir(t, project)
	mustMkdir(t, workflows)

	writeFile(t, filepath.Join(project, "a.sh"), "#!/bin/sh\ntouch \"$PWD/a.done\"\n")
	writeFile(t, filepath.Join(project, "b.sh"), "#!/bin/sh\ntouch \"$PWD/b.done\"\n")
	chmodX(t, filepath.Join(project, "a.sh"))
	chmodX(t, filepath.Join(project, "b.sh"))

	// eq and exists both match when kind is present → multi-match fail.
	writeFile(t, filepath.Join(workflows, "decision.yaml"), `
id: decision
steps:
  - id: set-kind
    mode: series
    outputs:
      - name: kind
        value: csv
    processes:
      - id: noop
        command: true
  - id: branch
    mode: decision
    inputs:
      - name: kind
        source:
          stepId: set-kind
          output: kind
    processes:
      - id: by-eq
        when:
          eq:
            input: kind
            value: csv
        command: ./a.sh
      - id: by-exists
        when:
          exists:
            input: kind
        command: ./b.sh
`)

	eng := engine.New(engine.Config{WorkflowsDir: workflows, StorageRoot: storage})
	run, err := eng.Start(engine.StartRequest{WorkflowID: "decision", ProjectPath: project})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	_ = waitStatus(t, eng, run.ID, engine.StatusFailed)
	if _, err := os.Stat(filepath.Join(project, "a.done")); err == nil {
		t.Fatal("process ran on multi-match")
	}
	if _, err := os.Stat(filepath.Join(project, "b.done")); err == nil {
		t.Fatal("process ran on multi-match")
	}
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

func waitStatus(t *testing.T, eng *engine.Engine, id, want string) *engine.WorkflowRun {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, err := eng.Get(id)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got.Status == want {
			return got
		}
		switch got.Status {
		case engine.StatusRunning, engine.StatusPaused:
			// transitional
		default:
			t.Fatalf("status=%s want %s", got.Status, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", want)
	return nil
}


func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
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

func TestWorkflowDefaultProjectWhenNoOverride(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "default-proj")
	workflows := filepath.Join(root, "workflows")
	storage := filepath.Join(root, "storage")
	mustMkdir(t, project)
	mustMkdir(t, workflows)

	writeFile(t, filepath.Join(project, "ok.sh"), "#!/bin/sh\necho ran\n")
	chmodX(t, filepath.Join(project, "ok.sh"))

	writeFile(t, filepath.Join(workflows, "with-default.yaml"), `
id: with-default
name: With Default
defaultProject: `+project+`
steps:
  - id: one
    mode: series
    processes:
      - id: ok
        command: ./ok.sh
`)

	eng := engine.New(engine.Config{WorkflowsDir: workflows, StorageRoot: storage})
	run, err := eng.Start(engine.StartRequest{WorkflowID: "with-default"})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	run = waitStatus(t, eng, run.ID, engine.StatusSucceeded)
	abs, _ := filepath.Abs(project)
	if run.ProjectPath != abs {
		t.Fatalf("ProjectPath=%q want %q", run.ProjectPath, abs)
	}
}

func TestStartTimeProjectOverrideWins(t *testing.T) {
	root := t.TempDir()
	defaultProj := filepath.Join(root, "default-proj")
	overrideProj := filepath.Join(root, "override-proj")
	workflows := filepath.Join(root, "workflows")
	storage := filepath.Join(root, "storage")
	mustMkdir(t, defaultProj)
	mustMkdir(t, overrideProj)
	mustMkdir(t, workflows)

	writeFile(t, filepath.Join(defaultProj, "ok.sh"), "#!/bin/sh\necho default\n")
	chmodX(t, filepath.Join(defaultProj, "ok.sh"))
	writeFile(t, filepath.Join(overrideProj, "ok.sh"), "#!/bin/sh\necho override > marker\n")
	chmodX(t, filepath.Join(overrideProj, "ok.sh"))

	writeFile(t, filepath.Join(workflows, "with-default.yaml"), `
id: with-default
defaultProject: `+defaultProj+`
steps:
  - id: one
    mode: series
    processes:
      - id: ok
        command: ./ok.sh
`)

	eng := engine.New(engine.Config{WorkflowsDir: workflows, StorageRoot: storage})
	run, err := eng.Start(engine.StartRequest{
		WorkflowID:  "with-default",
		ProjectPath: overrideProj,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	run = waitStatus(t, eng, run.ID, engine.StatusSucceeded)
	abs, _ := filepath.Abs(overrideProj)
	if run.ProjectPath != abs {
		t.Fatalf("ProjectPath=%q want %q", run.ProjectPath, abs)
	}
	if _, err := os.Stat(filepath.Join(overrideProj, "marker")); err != nil {
		t.Fatalf("override project script did not run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(defaultProj, "marker")); err == nil {
		t.Fatal("default project should not have been used")
	}
}

func TestPerRunInputOverride(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "proj")
	workflows := filepath.Join(root, "workflows")
	storage := filepath.Join(root, "storage")
	mustMkdir(t, project)
	mustMkdir(t, workflows)

	writeFile(t, filepath.Join(project, "check.sh"), `#!/bin/sh
test "$SUBSPACE_INPUT_msg" = "from-override" || { echo "got=$SUBSPACE_INPUT_msg" >&2; exit 1; }
echo ok
`)
	chmodX(t, filepath.Join(project, "check.sh"))

	writeFile(t, filepath.Join(workflows, "lit.yaml"), `
id: lit
steps:
  - id: use
    mode: series
    inputs:
      - name: msg
        value: from-yaml
    processes:
      - id: check
        command: ./check.sh
`)

	eng := engine.New(engine.Config{WorkflowsDir: workflows, StorageRoot: storage})
	run, err := eng.Start(engine.StartRequest{
		WorkflowID:  "lit",
		ProjectPath: project,
		InputOverrides: map[string]string{"use.msg": "from-override"},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	_ = waitStatus(t, eng, run.ID, engine.StatusSucceeded)
}

func TestPerRunArgumentOverride(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "proj")
	workflows := filepath.Join(root, "workflows")
	storage := filepath.Join(root, "storage")
	mustMkdir(t, project)
	mustMkdir(t, workflows)

	writeFile(t, filepath.Join(project, "echo.sh"), `#!/bin/sh
printf '%s\n' "$@" > out.txt
`)
	chmodX(t, filepath.Join(project, "echo.sh"))

	writeFile(t, filepath.Join(workflows, "args.yaml"), `
id: args
steps:
  - id: one
    mode: series
    processes:
      - id: echo
        command: ./echo.sh
        arguments: ["yaml-arg"]
`)

	eng := engine.New(engine.Config{WorkflowsDir: workflows, StorageRoot: storage})
	run, err := eng.Start(engine.StartRequest{
		WorkflowID:  "args",
		ProjectPath: project,
		ArgumentOverrides: map[string][]string{"echo": {"override-arg"}},
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	_ = waitStatus(t, eng, run.ID, engine.StatusSucceeded)
	got, err := os.ReadFile(filepath.Join(project, "out.txt"))
	if err != nil {
		t.Fatalf("read out: %v", err)
	}
	if strings.TrimSpace(string(got)) != "override-arg" {
		t.Fatalf("args=%q want override-arg", got)
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
	run = waitStatus(t, eng, run.ID, engine.StatusSucceeded)
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
	run = waitStatus(t, eng, run.ID, engine.StatusFailed)
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


func TestLoopForwardGoesToNextStep(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "proj")
	workflows := filepath.Join(root, "workflows")
	storage := filepath.Join(root, "storage")
	mustMkdir(t, project)
	mustMkdir(t, workflows)

	writeFile(t, filepath.Join(project, "a.sh"), "#!/bin/sh\necho a >> \"$PWD/log\"\n")
	writeFile(t, filepath.Join(project, "loop.sh"), "#!/bin/sh\necho loop >> \"$PWD/log\"\n")
	writeFile(t, filepath.Join(project, "b.sh"), "#!/bin/sh\necho b >> \"$PWD/log\"\n")
	chmodX(t, filepath.Join(project, "a.sh"))
	chmodX(t, filepath.Join(project, "loop.sh"))
	chmodX(t, filepath.Join(project, "b.sh"))

	writeFile(t, filepath.Join(workflows, "loop.yaml"), `
id: loop
steps:
  - id: before
    mode: series
    outputs:
      - name: cont
        value: no
    processes:
      - id: run-a
        command: ./a.sh
  - id: bounce
    mode: loop
    inputs:
      - name: cont
        source:
          stepId: before
          output: cont
    when:
      eq:
        input: cont
        value: yes
    processes:
      - id: run-loop
        command: ./loop.sh
  - id: after
    mode: series
    processes:
      - id: run-b
        command: ./b.sh
`)

	eng := engine.New(engine.Config{WorkflowsDir: workflows, StorageRoot: storage})
	run, err := eng.Start(engine.StartRequest{WorkflowID: "loop", ProjectPath: project})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	_ = waitStatus(t, eng, run.ID, engine.StatusSucceeded)
	log := readFile(t, filepath.Join(project, "log"))
	if strings.TrimSpace(log) != "a\nloop\nb" {
		t.Fatalf("log=%q want a, loop, b once each", log)
	}
}

func TestLoopBackwardGoesToPreviousStep(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "proj")
	workflows := filepath.Join(root, "workflows")
	storage := filepath.Join(root, "storage")
	mustMkdir(t, project)
	mustMkdir(t, workflows)

	writeFile(t, filepath.Join(project, "a.sh"), "#!/bin/sh\necho a >> \"$PWD/log\"\n")
	writeFile(t, filepath.Join(project, "loop.sh"), "#!/bin/sh\necho loop >> \"$PWD/log\"\n")
	writeFile(t, filepath.Join(project, "b.sh"), "#!/bin/sh\necho b >> \"$PWD/log\"\n")
	chmodX(t, filepath.Join(project, "a.sh"))
	chmodX(t, filepath.Join(project, "loop.sh"))
	chmodX(t, filepath.Join(project, "b.sh"))

	writeFile(t, filepath.Join(workflows, "loop.yaml"), `
id: loop
steps:
  - id: before
    mode: series
    outputs:
      - name: cont
        value: yes
    processes:
      - id: run-a
        command: ./a.sh
  - id: bounce
    mode: loop
    maxIterations: 2
    inputs:
      - name: cont
        source:
          stepId: before
          output: cont
    when:
      eq:
        input: cont
        value: yes
    processes:
      - id: run-loop
        command: ./loop.sh
  - id: after
    mode: series
    processes:
      - id: run-b
        command: ./b.sh
`)

	eng := engine.New(engine.Config{WorkflowsDir: workflows, StorageRoot: storage})
	run, err := eng.Start(engine.StartRequest{WorkflowID: "loop", ProjectPath: project})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	_ = waitStatus(t, eng, run.ID, engine.StatusFailed)
	log := readFile(t, filepath.Join(project, "log"))
	if strings.TrimSpace(log) != "a\nloop\na\nloop\na" {
		t.Fatalf("log=%q want a,loop,a,loop,a (backward then cap)", log)
	}
	if strings.Contains(log, "b") {
		t.Fatal("after step should not run")
	}
}

func TestLoopMaxIterationsFailsStepRun(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "proj")
	workflows := filepath.Join(root, "workflows")
	storage := filepath.Join(root, "storage")
	mustMkdir(t, project)
	mustMkdir(t, workflows)

	writeFile(t, filepath.Join(project, "a.sh"), "#!/bin/sh\necho a >> \"$PWD/log\"\n")
	writeFile(t, filepath.Join(project, "loop.sh"), "#!/bin/sh\necho loop >> \"$PWD/log\"\n")
	chmodX(t, filepath.Join(project, "a.sh"))
	chmodX(t, filepath.Join(project, "loop.sh"))

	writeFile(t, filepath.Join(workflows, "loop.yaml"), `
id: loop
steps:
  - id: before
    mode: series
    outputs:
      - name: cont
        value: yes
    processes:
      - id: run-a
        command: ./a.sh
  - id: bounce
    mode: loop
    inputs:
      - name: cont
        source:
          stepId: before
          output: cont
    when:
      eq:
        input: cont
        value: yes
    processes:
      - id: run-loop
        command: ./loop.sh
`)

	eng := engine.New(engine.Config{WorkflowsDir: workflows, StorageRoot: storage})
	run, err := eng.Start(engine.StartRequest{WorkflowID: "loop", ProjectPath: project})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	_ = waitStatus(t, eng, run.ID, engine.StatusFailed)
	log := readFile(t, filepath.Join(project, "log"))
	if strings.TrimSpace(log) != "a\nloop\na\nloop\na\nloop\na" {
		t.Fatalf("log=%q want 3 loop iterations then cap", log)
	}
}
