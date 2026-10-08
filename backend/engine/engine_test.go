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
	if run.Status != engine.StatusSucceeded {
		t.Fatalf("status=%s want succeeded", run.Status)
	}
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
	if run.Status != engine.StatusSucceeded {
		t.Fatalf("status=%s", run.Status)
	}
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
	if run.Status != engine.StatusSucceeded {
		t.Fatalf("status=%s want succeeded", run.Status)
	}
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
	if run.Status != engine.StatusSucceeded {
		t.Fatalf("status=%s", run.Status)
	}
	got, err := os.ReadFile(filepath.Join(project, "out.txt"))
	if err != nil {
		t.Fatalf("read out: %v", err)
	}
	if strings.TrimSpace(string(got)) != "override-arg" {
		t.Fatalf("args=%q want override-arg", got)
	}
}
