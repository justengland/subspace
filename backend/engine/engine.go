package engine

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusRunning   = "running"
)

type Config struct {
	WorkflowsDir string
	StorageRoot  string
}

type Engine struct {
	cfg  Config
	mu   sync.Mutex
	runs map[string]*runRec
}

type runRec struct {
	run  *WorkflowRun
	jl   *jsonl
	path string
	done chan struct{}
}

type StartRequest struct {
	WorkflowID  string `json:"workflowId"`
	ProjectPath string `json:"projectPath"`
}

type WorkflowRun struct {
	ID          string `json:"id"`
	WorkflowID  string `json:"workflowId"`
	ProjectPath string `json:"projectPath"`
	Status      string `json:"status"`
}

type workflowDef struct {
	ID    string    `yaml:"id"`
	Name  string    `yaml:"name"`
	Steps []stepDef `yaml:"steps"`
}

type stepDef struct {
	ID        string       `yaml:"id"`
	Name      string       `yaml:"name"`
	Mode      string       `yaml:"mode"`
	Processes []processDef `yaml:"processes"`
}

type processDef struct {
	ID               string   `yaml:"id"`
	Name             string   `yaml:"name"`
	Command          string   `yaml:"command"`
	Arguments        []string `yaml:"arguments"`
	WorkingDirectory string   `yaml:"workingDirectory"`
}

func New(cfg Config) *Engine {
	return &Engine{cfg: cfg, runs: map[string]*runRec{}}
}

func (e *Engine) Start(req StartRequest) (*WorkflowRun, error) {
	wf, err := e.loadWorkflow(req.WorkflowID)
	if err != nil {
		return nil, err
	}
	project, err := filepath.Abs(req.ProjectPath)
	if err != nil {
		return nil, err
	}
	id := shortID()
	run := &WorkflowRun{
		ID:          id,
		WorkflowID:  wf.ID,
		ProjectPath: project,
		Status:      StatusRunning,
	}
	runDir := filepath.Join(e.cfg.StorageRoot, filepath.Base(project), id)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return nil, err
	}
	jsonlPath := filepath.Join(runDir, "events.jsonl")
	jl, err := openJSONL(jsonlPath)
	if err != nil {
		return nil, err
	}
	rec := &runRec{run: run, jl: jl, path: jsonlPath, done: make(chan struct{})}
	e.mu.Lock()
	e.runs[id] = rec
	e.mu.Unlock()

	go e.execute(rec, wf, project)

	cp := *run
	return &cp, nil
}

func (e *Engine) execute(rec *runRec, wf *workflowDef, project string) {
	defer func() {
		_ = rec.jl.Close()
		close(rec.done)
	}()
	jl := rec.jl
	run := rec.run

	_ = jl.Append(map[string]any{"type": "workflow_started", "workflowId": wf.ID, "runId": run.ID})

	for _, step := range wf.Steps {
		mode := step.Mode
		if mode == "" {
			mode = "series"
		}
		_ = jl.Append(map[string]any{"type": "step_started", "stepId": step.ID, "mode": mode})
		switch mode {
		case "series":
			if err := e.runSeries(project, step, jl); err != nil {
				run.Status = StatusFailed
				_ = jl.Append(map[string]any{"type": "workflow_failed", "error": err.Error()})
				return
			}
		default:
			err := fmt.Errorf("unsupported mode %q", mode)
			run.Status = StatusFailed
			_ = jl.Append(map[string]any{"type": "workflow_failed", "error": err.Error()})
			return
		}
		_ = jl.Append(map[string]any{"type": "step_succeeded", "stepId": step.ID})
	}

	run.Status = StatusSucceeded
	_ = jl.Append(map[string]any{"type": "workflow_succeeded", "runId": run.ID})
}

func (e *Engine) Get(id string) (*WorkflowRun, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	rec, ok := e.runs[id]
	if !ok {
		return nil, fmt.Errorf("WorkflowRun %q not found", id)
	}
	cp := *rec.run
	return &cp, nil
}

// Follow sends JSONL history then live appends (same map shape as the file).
// The channel closes when the run finishes (or immediately after history if already done).
func (e *Engine) Follow(id string) (<-chan map[string]any, func(), error) {
	e.mu.Lock()
	rec, ok := e.runs[id]
	e.mu.Unlock()
	if !ok {
		return nil, nil, fmt.Errorf("WorkflowRun %q not found", id)
	}

	out := make(chan map[string]any, 256)
	stop := make(chan struct{})
	var once sync.Once
	cancel := func() { once.Do(func() { close(stop) }) }

	go func() {
		defer close(out)
		hist, live, unsub := rec.jl.Subscribe()
		defer unsub()
		for _, ev := range hist {
			select {
			case out <- ev:
			case <-stop:
				return
			}
		}
		for {
			select {
			case ev, ok := <-live:
				if !ok {
					return
				}
				select {
				case out <- ev:
				case <-stop:
					return
				}
			case <-stop:
				return
			}
		}
	}()
	return out, cancel, nil
}

func (e *Engine) loadWorkflow(id string) (*workflowDef, error) {
	path := filepath.Join(e.cfg.WorkflowsDir, id+".yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load workflow %q: %w", id, err)
	}
	var wf workflowDef
	if err := yaml.Unmarshal(data, &wf); err != nil {
		return nil, err
	}
	if wf.ID == "" {
		wf.ID = id
	}
	return &wf, nil
}

func (e *Engine) runSeries(project string, step stepDef, jl *jsonl) error {
	for _, p := range step.Processes {
		if err := e.runProcess(project, p, jl); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) runProcess(project string, p processDef, jl *jsonl) error {
	_ = jl.Append(map[string]any{"type": "process_started", "processId": p.ID, "command": p.Command})

	cwd := project
	if p.WorkingDirectory != "" {
		if filepath.IsAbs(p.WorkingDirectory) {
			cwd = p.WorkingDirectory
		} else {
			cwd = filepath.Join(project, p.WorkingDirectory)
		}
	}

	cmd := exec.Command(p.Command, p.Arguments...)
	cmd.Dir = cwd

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); pipeLines(jl, "stdout", p.ID, stdout) }()
	go func() { defer wg.Done(); pipeLines(jl, "stderr", p.ID, stderr) }()
	wg.Wait()

	err = cmd.Wait()
	exit := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else {
			_ = jl.Append(map[string]any{"type": "process_failed", "processId": p.ID, "error": err.Error()})
			return err
		}
	}
	if exit != 0 {
		_ = jl.Append(map[string]any{"type": "process_failed", "processId": p.ID, "exitCode": exit})
		return fmt.Errorf("process %s exited %d", p.ID, exit)
	}
	_ = jl.Append(map[string]any{"type": "process_succeeded", "processId": p.ID, "exitCode": 0})
	return nil
}

func pipeLines(jl *jsonl, stream, processID string, r io.Reader) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		_ = jl.Append(map[string]any{
			"type":      stream,
			"processId": processID,
			"data":      sc.Text(),
		})
	}
}

func shortID() string {
	now := time.Now().UTC()
	return fmt.Sprintf("%s-%04x", now.Format("20060102-150405"), now.Nanosecond()&0xffff)
}
