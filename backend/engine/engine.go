package engine

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	StatusSucceeded = "succeeded"
	StatusFailed    = "failed"
	StatusRunning   = "running"
	StatusStopped   = "stopped"
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

type ProcessRun struct {
	ProcessID string `json:"processId"`
	Status    string `json:"status"`
	ExitCode  int    `json:"exitCode,omitempty"`
}

type WorkflowRun struct {
	ID          string       `json:"id"`
	WorkflowID  string       `json:"workflowId"`
	ProjectPath string       `json:"projectPath"`
	Status      string       `json:"status"`
	ProcessRuns []ProcessRun `json:"processRuns,omitempty"`
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
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Steps       []StepView   `json:"steps"`
	Connections []Connection `json:"connections"`
}

type StepView struct {
	ID      string       `json:"id"`
	Name    string       `json:"name"`
	Mode    string       `json:"mode"`
	Inputs  []InputView  `json:"inputs,omitempty"`
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
	ID    string    `yaml:"id"`
	Name  string    `yaml:"name"`
	Steps []stepDef `yaml:"steps"`
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
	ID               string        `yaml:"id"`
	Name             string        `yaml:"name"`
	Command          string        `yaml:"command"`
	Arguments        []string      `yaml:"arguments"`
	WorkingDirectory string        `yaml:"workingDirectory"`
	When             *predicateDef `yaml:"when"`
}

// Structured YAML predicates (eq / in / exists). Exactly one form per when.
type predicateDef struct {
	Eq     *eqPred     `yaml:"eq"`
	In     *inPred     `yaml:"in"`
	Exists *existsPred `yaml:"exists"`
}

type eqPred struct {
	Input string `yaml:"input"`
	Value string `yaml:"value"`
}

type inPred struct {
	Input  string   `yaml:"input"`
	Values []string `yaml:"values"`
}

type existsPred struct {
	Input string `yaml:"input"`
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

	// stepID -> outputName -> value (populated as Steps complete)
	outputs := map[string]map[string]string{}

	for _, step := range wf.Steps {
		mode := step.Mode
		if mode == "" {
			mode = "series"
		}
		inputs, env, err := resolveInputs(step, outputs)
		if err != nil {
			run.Status = StatusFailed
			_ = jl.Append(map[string]any{"type": "workflow_failed", "error": err.Error()})
			return run, nil
		}
		_ = jl.Append(map[string]any{"type": "step_started", "stepId": step.ID, "mode": mode})
		var stepErr error
		switch mode {
		case "series":
			stepErr = e.runSeries(run, project, step, env, jl)
		case "parallel":
			stepErr = e.runParallel(run, project, step, env, jl)
		case "decision":
			stepErr = e.runDecision(run, project, step, inputs, env, jl)
		default:
			stepErr = fmt.Errorf("unsupported mode %q", mode)
		}
		if stepErr != nil {
			run.Status = StatusFailed
			_ = jl.Append(map[string]any{"type": "workflow_failed", "error": stepErr.Error()})
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
	cp.ProcessRuns = append([]ProcessRun(nil), run.ProcessRuns...)
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
	out := &Workflow{ID: wf.ID, Name: wf.Name}
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

func resolveInputs(step stepDef, outputs map[string]map[string]string) (map[string]string, []string, error) {
	inputs := map[string]string{}
	var env []string
	for _, in := range step.Inputs {
		if in.Source == nil {
			continue
		}
		stepOut, ok := outputs[in.Source.StepID]
		if !ok {
			return nil, nil, fmt.Errorf("input %q: step %q has no outputs yet", in.Name, in.Source.StepID)
		}
		val, ok := stepOut[in.Source.Output]
		if !ok {
			return nil, nil, fmt.Errorf("input %q: output %q.%q not found", in.Name, in.Source.StepID, in.Source.Output)
		}
		inputs[in.Name] = val
		env = append(env, fmt.Sprintf("SUBSPACE_INPUT_%s=%s", in.Name, val))
	}
	return inputs, env, nil
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

func (e *Engine) runSeries(run *WorkflowRun, project string, step stepDef, env []string, jl *jsonl) error {
	for _, p := range step.Processes {
		pr, err := e.runProcess(project, p, env, jl, nil)
		run.ProcessRuns = append(run.ProcessRuns, pr)
		if err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) runParallel(run *WorkflowRun, project string, step stepDef, env []string, jl *jsonl) error {
	type slot struct {
		def  processDef
		cmd  *exec.Cmd
		term atomic.Bool
		pr   ProcessRun
	}
	slots := make([]*slot, len(step.Processes))
	for i, p := range step.Processes {
		cmd, err := e.startCmd(project, p, env, jl)
		if err != nil {
			return err
		}
		slots[i] = &slot{def: p, cmd: cmd}
	}

	var failOnce sync.Once
	var firstErr error
	var mu sync.Mutex
	sigtermSiblings := func(failed *slot) {
		failOnce.Do(func() {
			for _, s := range slots {
				if s == failed || s.cmd.Process == nil {
					continue
				}
				s.term.Store(true)
				_ = syscall.Kill(-s.cmd.Process.Pid, syscall.SIGTERM)
				_ = jl.Append(map[string]any{"type": "process_sigterm", "processId": s.def.ID})
			}
		})
	}

	var wg sync.WaitGroup
	for _, s := range slots {
		wg.Add(1)
		go func(s *slot) {
			defer wg.Done()
			pr, err := e.waitCmd(s.def, s.cmd, jl, &s.term)
			mu.Lock()
			s.pr = pr
			if err != nil && pr.Status != StatusStopped {
				if firstErr == nil {
					firstErr = err
				}
				mu.Unlock()
				sigtermSiblings(s)
				return
			}
			mu.Unlock()
		}(s)
	}
	wg.Wait()

	for _, s := range slots {
		run.ProcessRuns = append(run.ProcessRuns, s.pr)
	}
	return firstErr
}

func (e *Engine) runDecision(run *WorkflowRun, project string, step stepDef, inputs map[string]string, env []string, jl *jsonl) error {
	var matched []processDef
	for _, p := range step.Processes {
		ok, err := predicateMatches(p.When, inputs)
		if err != nil {
			return err
		}
		if ok {
			matched = append(matched, p)
		}
	}
	if len(matched) == 0 {
		return fmt.Errorf("decision step %q: zero processes matched", step.ID)
	}
	if len(matched) > 1 {
		ids := make([]string, len(matched))
		for i, p := range matched {
			ids[i] = p.ID
		}
		return fmt.Errorf("decision step %q: multiple processes matched: %v", step.ID, ids)
	}
	pr, err := e.runProcess(project, matched[0], env, jl, nil)
	run.ProcessRuns = append(run.ProcessRuns, pr)
	return err
}

func predicateMatches(p *predicateDef, inputs map[string]string) (bool, error) {
	if p == nil {
		return false, nil
	}
	n := 0
	if p.Eq != nil {
		n++
	}
	if p.In != nil {
		n++
	}
	if p.Exists != nil {
		n++
	}
	if n != 1 {
		return false, fmt.Errorf("predicate must set exactly one of eq/in/exists")
	}
	switch {
	case p.Eq != nil:
		v, ok := inputs[p.Eq.Input]
		return ok && v == p.Eq.Value, nil
	case p.In != nil:
		v, ok := inputs[p.In.Input]
		if !ok {
			return false, nil
		}
		for _, want := range p.In.Values {
			if v == want {
				return true, nil
			}
		}
		return false, nil
	default:
		_, ok := inputs[p.Exists.Input]
		return ok, nil
	}
}

func (e *Engine) runProcess(project string, p processDef, env []string, jl *jsonl, termFlag *atomic.Bool) (ProcessRun, error) {
	cmd, err := e.startCmd(project, p, env, jl)
	if err != nil {
		return ProcessRun{ProcessID: p.ID, Status: StatusFailed}, err
	}
	return e.waitCmd(p, cmd, jl, termFlag)
}

func (e *Engine) startCmd(project string, p processDef, env []string, jl *jsonl) (*exec.Cmd, error) {
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
	if len(env) > 0 {
		cmd.Env = append(os.Environ(), env...)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}

	go pipeLines(jl, "stdout", p.ID, stdout)
	go pipeLines(jl, "stderr", p.ID, stderr)
	return cmd, nil
}

func (e *Engine) waitCmd(p processDef, cmd *exec.Cmd, jl *jsonl, termFlag *atomic.Bool) (ProcessRun, error) {
	err := cmd.Wait()
	exit := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			exit = ee.ExitCode()
		} else {
			_ = jl.Append(map[string]any{"type": "process_failed", "processId": p.ID, "error": err.Error()})
			return ProcessRun{ProcessID: p.ID, Status: StatusFailed}, err
		}
	}

	if termFlag != nil && termFlag.Load() {
		_ = jl.Append(map[string]any{"type": "process_stopped", "processId": p.ID, "exitCode": exit})
		return ProcessRun{ProcessID: p.ID, Status: StatusStopped, ExitCode: exit}, fmt.Errorf("process %s stopped", p.ID)
	}
	if exit != 0 {
		_ = jl.Append(map[string]any{"type": "process_failed", "processId": p.ID, "exitCode": exit})
		return ProcessRun{ProcessID: p.ID, Status: StatusFailed, ExitCode: exit}, fmt.Errorf("process %s exited %d", p.ID, exit)
	}
	_ = jl.Append(map[string]any{"type": "process_succeeded", "processId": p.ID, "exitCode": 0})
	return ProcessRun{ProcessID: p.ID, Status: StatusSucceeded, ExitCode: 0}, nil
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
