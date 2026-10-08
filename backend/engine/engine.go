package engine

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusRunning   = "running"
	StatusPaused    = "paused"
	StatusStopped   = "stopped"
)

type Config struct {
	WorkflowsDir string
	StorageRoot  string
}

type Engine struct {
	cfg  Config
	mu   sync.Mutex
	runs map[string]*activeRun
}

type activeRun struct {
	run *WorkflowRun

	mu       sync.Mutex
	pauseReq bool
	stopped  bool
	resumeCh chan struct{}
	cmd      *exec.Cmd
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
	return &Engine{cfg: cfg, runs: map[string]*activeRun{}}
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
	ar := &activeRun{run: run, resumeCh: make(chan struct{})}
	e.mu.Lock()
	e.runs[id] = ar
	e.mu.Unlock()

	runDir := filepath.Join(e.cfg.StorageRoot, filepath.Base(project), id)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return nil, err
	}
	jl, err := openJSONL(filepath.Join(runDir, "events.jsonl"))
	if err != nil {
		return nil, err
	}

	go e.execute(ar, wf, project, jl)
	cp := *run
	return &cp, nil
}

func (e *Engine) Get(id string) (*WorkflowRun, error) {
	ar, err := e.getActive(id)
	if err != nil {
		return nil, err
	}
	ar.mu.Lock()
	defer ar.mu.Unlock()
	cp := *ar.run
	return &cp, nil
}

func (e *Engine) Pause(id string) error {
	ar, err := e.getActive(id)
	if err != nil {
		return err
	}
	ar.mu.Lock()
	defer ar.mu.Unlock()
	if ar.stopped {
		return fmt.Errorf("WorkflowRun %q is stopped", id)
	}
	ar.pauseReq = true
	return nil
}

func (e *Engine) Resume(id string) error {
	ar, err := e.getActive(id)
	if err != nil {
		return err
	}
	ar.mu.Lock()
	defer ar.mu.Unlock()
	if ar.run.Status != StatusPaused {
		return fmt.Errorf("WorkflowRun %q is not paused", id)
	}
	ar.pauseReq = false
	ar.run.Status = StatusRunning
	select {
	case ar.resumeCh <- struct{}{}:
	default:
	}
	return nil
}

func (e *Engine) Stop(id string) error {
	ar, err := e.getActive(id)
	if err != nil {
		return err
	}
	ar.mu.Lock()
	ar.stopped = true
	ar.pauseReq = false
	cmd := ar.cmd
	if ar.run.Status == StatusPaused {
		select {
		case ar.resumeCh <- struct{}{}:
		default:
		}
	}
	ar.mu.Unlock()
	sigterm(cmd)
	return nil
}

func sigterm(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	// Process group so sleep/children get SIGTERM too.
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
}

func (e *Engine) getActive(id string) (*activeRun, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	ar, ok := e.runs[id]
	if !ok {
		return nil, fmt.Errorf("WorkflowRun %q not found", id)
	}
	return ar, nil
}

func (e *Engine) execute(ar *activeRun, wf *workflowDef, project string, jl *jsonl) {
	defer jl.Close()
	_ = jl.Append(map[string]any{"type": "workflow_started", "workflowId": wf.ID, "runId": ar.run.ID})

	for _, step := range wf.Steps {
		if ar.isStopped() {
			_ = jl.Append(map[string]any{"type": "workflow_stopped", "runId": ar.run.ID})
			ar.setStatus(StatusStopped)
			return
		}
		if err := ar.waitIfPaused(); err != nil {
			_ = jl.Append(map[string]any{"type": "workflow_stopped", "runId": ar.run.ID})
			ar.setStatus(StatusStopped)
			return
		}

		mode := step.Mode
		if mode == "" {
			mode = "series"
		}
		_ = jl.Append(map[string]any{"type": "step_started", "stepId": step.ID, "mode": mode})
		switch mode {
		case "series":
			if err := e.runSeries(ar, project, step, jl); err != nil {
				if ar.isStopped() {
					_ = jl.Append(map[string]any{"type": "workflow_stopped", "runId": ar.run.ID})
					ar.setStatus(StatusStopped)
					return
				}
				ar.setStatus(StatusFailed)
				_ = jl.Append(map[string]any{"type": "workflow_failed", "error": err.Error()})
				return
			}
		default:
			err := fmt.Errorf("unsupported mode %q", mode)
			ar.setStatus(StatusFailed)
			_ = jl.Append(map[string]any{"type": "workflow_failed", "error": err.Error()})
			return
		}
		_ = jl.Append(map[string]any{"type": "step_succeeded", "stepId": step.ID})

		// Hold after current Step's ProcessRuns finish, before next Step.
		if err := ar.waitIfPaused(); err != nil {
			_ = jl.Append(map[string]any{"type": "workflow_stopped", "runId": ar.run.ID})
			ar.setStatus(StatusStopped)
			return
		}
	}

	ar.setStatus(StatusSucceeded)
	_ = jl.Append(map[string]any{"type": "workflow_succeeded", "runId": ar.run.ID})
}

func (ar *activeRun) setStatus(status string) {
	ar.mu.Lock()
	ar.run.Status = status
	ar.mu.Unlock()
}

func (ar *activeRun) isStopped() bool {
	ar.mu.Lock()
	defer ar.mu.Unlock()
	return ar.stopped
}

func (ar *activeRun) waitIfPaused() error {
	ar.mu.Lock()
	if ar.stopped {
		ar.mu.Unlock()
		return fmt.Errorf("stopped")
	}
	if !ar.pauseReq {
		ar.mu.Unlock()
		return nil
	}
	ar.run.Status = StatusPaused
	ch := ar.resumeCh
	ar.mu.Unlock()
	<-ch
	ar.mu.Lock()
	stopped := ar.stopped
	if !stopped {
		ar.run.Status = StatusRunning
	}
	ar.mu.Unlock()
	if stopped {
		return fmt.Errorf("stopped")
	}
	return nil
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

func (e *Engine) runSeries(ar *activeRun, project string, step stepDef, jl *jsonl) error {
	for _, p := range step.Processes {
		if ar.isStopped() {
			return fmt.Errorf("stopped")
		}
		if err := e.runProcess(ar, project, p, jl); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) runProcess(ar *activeRun, project string, p processDef, jl *jsonl) error {
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
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

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

	ar.mu.Lock()
	ar.cmd = cmd
	stopped := ar.stopped
	ar.mu.Unlock()
	if stopped {
		sigterm(cmd)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); pipeLines(jl, "stdout", p.ID, stdout) }()
	go func() { defer wg.Done(); pipeLines(jl, "stderr", p.ID, stderr) }()
	wg.Wait()

	err = cmd.Wait()
	ar.mu.Lock()
	ar.cmd = nil
	stopped = ar.stopped
	ar.mu.Unlock()
	if stopped {
		_ = jl.Append(map[string]any{"type": "process_stopped", "processId": p.ID})
		return fmt.Errorf("stopped")
	}

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
