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
	WorkflowID         string              `json:"workflowId"`
	ProjectPath        string              `json:"projectPath,omitempty"` // empty → Workflow.defaultProject
	InputOverrides     map[string]string   `json:"inputOverrides,omitempty"`     // "stepId.inputName" → value
	ArgumentOverrides  map[string][]string `json:"argumentOverrides,omitempty"`  // processId → args
}

type WorkflowRun struct {
	ID          string `json:"id"`
	WorkflowID  string `json:"workflowId"`
	ProjectPath string `json:"projectPath"`
	Status      string `json:"status"`
}

// Connection is a derived edge from Input.source (not stored in YAML).
type Connection struct {
	SourceStepID string `json:"sourceStepId"`
	SourceOutput string `json:"sourceOutput"`
	TargetStepID string `json:"targetStepId"`
	TargetInput  string `json:"targetInput"`
}

// Workflow is the Engine-facing definition view (Connections derived on read).
type Workflow struct {
	ID             string       `json:"id"`
	Name           string       `json:"name"`
	DefaultProject string       `json:"defaultProject,omitempty"`
	Steps          []StepView   `json:"steps"`
	Connections    []Connection `json:"connections"`
}

type StepView struct {
	ID      string      `json:"id"`
	Name    string      `json:"name"`
	Mode    string      `json:"mode"`
	Inputs  []InputView `json:"inputs,omitempty"`
	Outputs []OutputView `json:"outputs,omitempty"`
}

type InputView struct {
	Name   string      `json:"name"`
	Source *SourceView `json:"source,omitempty"`
}

type OutputView struct {
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`
}

type SourceView struct {
	StepID string `json:"stepId"`
	Output string `json:"output"`
}

type workflowDef struct {
	ID             string    `yaml:"id"`
	Name           string    `yaml:"name"`
	DefaultProject string    `yaml:"defaultProject"`
	Steps          []stepDef `yaml:"steps"`
}

type stepDef struct {
	ID        string       `yaml:"id"`
	Name      string       `yaml:"name"`
	Mode      string       `yaml:"mode"`
	Inputs    []inputDef   `yaml:"inputs"`
	Outputs   []outputDef  `yaml:"outputs"`
	Processes []processDef `yaml:"processes"`
}

type inputDef struct {
	Name   string     `yaml:"name"`
	Value  string     `yaml:"value"`
	Source *sourceDef `yaml:"source"`
}

type outputDef struct {
	Name  string `yaml:"name"`
	Value string `yaml:"value"`
}

type sourceDef struct {
	StepID string `yaml:"stepId"`
	Output string `yaml:"output"`
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
	projectPath := req.ProjectPath
	if projectPath == "" {
		projectPath = wf.DefaultProject
	}
	if projectPath == "" {
		return nil, fmt.Errorf("project path required (no Workflow defaultProject)")
	}
	project, err := filepath.Abs(projectPath)
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

	// stepID -> outputName -> value (populated as Steps complete)
	outputs := map[string]map[string]string{}

	for _, step := range wf.Steps {
		mode := step.Mode
		if mode == "" {
			mode = "series"
		}
		env, err := resolveInputs(step, outputs, req.InputOverrides)
		if err != nil {
			run.Status = StatusFailed
			_ = jl.Append(map[string]any{"type": "workflow_failed", "error": err.Error()})
			return run, nil
		}
		_ = jl.Append(map[string]any{"type": "step_started", "stepId": step.ID, "mode": mode})
		switch mode {
		case "series":
			if err := e.runSeries(project, step, env, req.ArgumentOverrides, jl); err != nil {
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
		stepOut := map[string]string{}
		for _, o := range step.Outputs {
			stepOut[o.Name] = o.Value
		}
		outputs[step.ID] = stepOut
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

func (e *Engine) GetWorkflow(id string) (*Workflow, error) {
	wf, err := e.loadWorkflow(id)
	if err != nil {
		return nil, err
	}
	return toWorkflowView(wf), nil
}

func toWorkflowView(wf *workflowDef) *Workflow {
	out := &Workflow{ID: wf.ID, Name: wf.Name, DefaultProject: wf.DefaultProject}
	for _, s := range wf.Steps {
		sv := StepView{ID: s.ID, Name: s.Name, Mode: s.Mode}
		for _, in := range s.Inputs {
			iv := InputView{Name: in.Name}
			if in.Source != nil {
				iv.Source = &SourceView{StepID: in.Source.StepID, Output: in.Source.Output}
			}
			sv.Inputs = append(sv.Inputs, iv)
		}
		for _, o := range s.Outputs {
			sv.Outputs = append(sv.Outputs, OutputView{Name: o.Name, Value: o.Value})
		}
		out.Steps = append(out.Steps, sv)
		for _, in := range s.Inputs {
			if in.Source == nil {
				continue
			}
			out.Connections = append(out.Connections, Connection{
				SourceStepID: in.Source.StepID,
				SourceOutput: in.Source.Output,
				TargetStepID: s.ID,
				TargetInput:  in.Name,
			})
		}
	}
	if out.Connections == nil {
		out.Connections = []Connection{}
	}
	return out
}

func resolveInputs(step stepDef, outputs map[string]map[string]string, overrides map[string]string) ([]string, error) {
	var env []string
	for _, in := range step.Inputs {
		key := step.ID + "." + in.Name
		if ov, ok := overrides[key]; ok {
			env = append(env, fmt.Sprintf("SUBSPACE_INPUT_%s=%s", in.Name, ov))
			continue
		}
		if in.Source != nil {
			stepOut, ok := outputs[in.Source.StepID]
			if !ok {
				return nil, fmt.Errorf("input %q: step %q has no outputs yet", in.Name, in.Source.StepID)
			}
			val, ok := stepOut[in.Source.Output]
			if !ok {
				return nil, fmt.Errorf("input %q: output %q.%q not found", in.Name, in.Source.StepID, in.Source.Output)
			}
			env = append(env, fmt.Sprintf("SUBSPACE_INPUT_%s=%s", in.Name, val))
			continue
		}
		if in.Value != "" {
			env = append(env, fmt.Sprintf("SUBSPACE_INPUT_%s=%s", in.Name, in.Value))
		}
	}
	return env, nil
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

func (e *Engine) runSeries(project string, step stepDef, env []string, argOverrides map[string][]string, jl *jsonl) error {
	for _, p := range step.Processes {
		if err := e.runProcess(project, p, env, argOverrides, jl); err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) runProcess(project string, p processDef, env []string, argOverrides map[string][]string, jl *jsonl) error {
	_ = jl.Append(map[string]any{"type": "process_started", "processId": p.ID, "command": p.Command})

	cwd := project
	if p.WorkingDirectory != "" {
		if filepath.IsAbs(p.WorkingDirectory) {
			cwd = p.WorkingDirectory
		} else {
			cwd = filepath.Join(project, p.WorkingDirectory)
		}
	}

	args := p.Arguments
	if ov, ok := argOverrides[p.ID]; ok {
		args = ov
	}
	cmd := exec.Command(p.Command, args...)
	cmd.Dir = cwd
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}

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
