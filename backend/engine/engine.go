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
	runs map[string]*WorkflowRun
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
	return &Engine{cfg: cfg, runs: map[string]*WorkflowRun{}}
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
	e.mu.Lock()
	e.runs[id] = run
	e.mu.Unlock()

	runDir := filepath.Join(e.cfg.StorageRoot, filepath.Base(project), id)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return nil, err
	}
	jl, err := openJSONL(filepath.Join(runDir, "events.jsonl"))
	if err != nil {
		return nil, err
	}
	defer jl.Close()

	_ = jl.Append(map[string]any{"type": "workflow_started", "workflowId": wf.ID, "runId": id})

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
				return run, nil
			}
		default:
			err := fmt.Errorf("unsupported mode %q", mode)
			run.Status = StatusFailed
			_ = jl.Append(map[string]any{"type": "workflow_failed", "error": err.Error()})
			return run, nil
		}
		_ = jl.Append(map[string]any{"type": "step_succeeded", "stepId": step.ID})
	}

	run.Status = StatusSucceeded
	_ = jl.Append(map[string]any{"type": "workflow_succeeded", "runId": id})
	return run, nil
}

func (e *Engine) Get(id string) (*WorkflowRun, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	run, ok := e.runs[id]
	if !ok {
		return nil, fmt.Errorf("WorkflowRun %q not found", id)
	}
	cp := *run
	return &cp, nil
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
	// time-sortable short id: YYYYMMDD-HHMMSS-XXXX
	now := time.Now().UTC()
	return fmt.Sprintf("%s-%04x", now.Format("20060102-150405"), now.Nanosecond()&0xffff)
}
