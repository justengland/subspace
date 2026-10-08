package engine_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/justengland/subspace/backend/engine"
)

func TestFollowStreamsStdoutSameShapeAsJSONL(t *testing.T) {
	root := t.TempDir()
	project := filepath.Join(root, "proj")
	workflows := filepath.Join(root, "workflows")
	storage := filepath.Join(root, "storage")
	mustMkdir(t, project)
	mustMkdir(t, workflows)
	writeFile(t, filepath.Join(project, "slow.sh"), "#!/bin/sh\necho hello\nsleep 0.15\necho world\n")
	chmodX(t, filepath.Join(project, "slow.sh"))
	writeFile(t, filepath.Join(workflows, "slow.yaml"), `
id: slow
steps:
  - id: s
    mode: series
    processes:
      - id: p
        command: ./slow.sh
`)

	eng := engine.New(engine.Config{WorkflowsDir: workflows, StorageRoot: storage})
	run, err := eng.Start(engine.StartRequest{WorkflowID: "slow", ProjectPath: project})
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != engine.StatusRunning {
		t.Fatalf("Start status=%s want running", run.Status)
	}

	ch, cancel, err := eng.Follow(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()

	var types []string
	var stdout []string
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				goto done
			}
			typ, _ := ev["type"].(string)
			types = append(types, typ)
			if typ == "stdout" {
				stdout = append(stdout, ev["data"].(string))
			}
		case <-deadline:
			t.Fatal("Follow timed out")
		}
	}
done:
	joined := strings.Join(types, ",")
	for _, want := range []string{"workflow_started", "stdout", "workflow_succeeded"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("missing %q in %v", want, types)
		}
	}
	if len(stdout) < 2 || stdout[0] != "hello" || stdout[len(stdout)-1] != "world" {
		t.Fatalf("stdout=%v", stdout)
	}

	// Reconnect / history: same shape from file.
	ch2, cancel2, err := eng.Follow(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel2()
	var again []string
	for ev := range ch2 {
		again = append(again, ev["type"].(string))
	}
	if !strings.Contains(strings.Join(again, ","), "stdout") ||
		!strings.Contains(strings.Join(again, ","), "workflow_succeeded") {
		t.Fatalf("history Follow=%v", again)
	}

	data, err := os.ReadFile(filepath.Join(storage, "proj", run.ID, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 {
		t.Fatal("empty JSONL")
	}
}
