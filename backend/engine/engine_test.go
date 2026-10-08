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
