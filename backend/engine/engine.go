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
	runs map[string]*runRec
}

type runRec struct {
	run  *WorkflowRun
	jl   *jsonl
	path string
	done chan struct{}
}

type StartRequest struct {
	WorkflowID        string              `json:"workflowId"`
	ProjectPath       string              `json:"projectPath,omitempty"`      // empty → Workflow.defaultProject
	InputOverrides    map[string]string   `json:"inputOverrides,omitempty"`   // "stepId.inputName" → value
	ArgumentOverrides map[string][]string `json:"argumentOverrides,omitempty"` // processId → args
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
	ID             string       `json:"id"`
	Name           string       `json:"name"`
	DefaultProject string       `json:"defaultProject,omitempty"`
	Steps          []StepView   `json:"steps"`
	Connections    []Connection `json:"connections"`
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
	ID             string    `yaml:"id"`
	Name           string    `yaml:"name"`
	DefaultProject string    `yaml:"defaultProject"`
	Steps          []stepDef `yaml:"steps"`
}

type stepDef struct {
	ID            string        `yaml:"id"`
	Name          string        `yaml:"name"`
	Mode          string        `yaml:"mode"`
	MaxIterations *int          `yaml:"maxIterations"`
	When          *predicateDef `yaml:"when"`
	Inputs        []inputDef    `yaml:"inputs"`
	Outputs       []outputDef   `yaml:"outputs"`
	Processes     []processDef  `yaml:"processes"`
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
	return &Engine{cfg: cfg, runs: map[string]*runRec{}}
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

	go e.execute(rec, wf, project, req)

	cp := *run
	return &cp, nil
}

func (e *Engine) execute(rec *runRec, wf *workflowDef, project string, req StartRequest) {
	defer func() {
		_ = rec.jl.Close()
		close(rec.done)
	}()
	jl := rec.jl
	run := rec.run

	_ = jl.Append(map[string]any{"type": "workflow_started", "workflowId": wf.ID, "runId": run.ID})

	outputs := map[string]map[string]string{}
	loopIters := map[string]int{}

	for i := 0; i < len(wf.Steps); {
		step := wf.Steps[i]
		mode := step.Mode
		if mode == "" {
			mode = "series"
		}
		inputs, env, err := resolveInputs(step, outputs, req.InputOverrides)
		if err != nil {
			run.Status = StatusFailed
			_ = jl.Append(map[string]any{"type": "workflow_failed", "error": err.Error()})
			return
		}

		iteration := 0
		if mode == "loop" {
			loopIters[step.ID]++
			iteration = loopIters[step.ID]
			max := 3
			if step.MaxIterations != nil {
				max = *step.MaxIterations
			}
			if iteration > max {
				err := fmt.Errorf("loop step %q: maxIterations %d exceeded", step.ID, max)
				run.Status = StatusFailed
				_ = jl.Append(map[string]any{"type": "step_started", "stepId": step.ID, "mode": mode, "iteration": iteration})
				_ = jl.Append(map[string]any{"type": "workflow_failed", "error": err.Error()})
				return
			}
		}

		ev := map[string]any{"type": "step_started", "stepId": step.ID, "mode": mode}
		if iteration > 0 {
			ev["iteration"] = iteration
		}
		_ = jl.Append(ev)

		var delta int
		var stepErr error
		switch mode {
		case "series":
			stepErr = e.runSeries(run, project, step, env, req.ArgumentOverrides, jl)
			delta = 1
		case "parallel":
			stepErr = e.runParallel(run, project, step, env, req.ArgumentOverrides, jl)
			delta = 1
		case "decision":
			stepErr = e.runDecision(run, project, step, inputs, env, req.ArgumentOverrides, jl)
			delta = 1
		case "loop":
			delta, stepErr = e.runLoop(run, project, step, inputs, env, req.ArgumentOverrides, jl, i)
		default:
			stepErr = fmt.Errorf("unsupported mode %q", mode)
		}
		if stepErr != nil {
			run.Status = StatusFailed
			_ = jl.Append(map[string]any{"type": "workflow_failed", "error": stepErr.Error()})
			return
		}
		stepOut := map[string]string{}
		for _, o := range step.Outputs {
			stepOut[o.Name] = o.Value
		}
		outputs[step.ID] = stepOut
		_ = jl.Append(map[string]any{"type": "step_succeeded", "stepId": step.ID})
		i += delta
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
	cp.ProcessRuns = append([]ProcessRun(nil), rec.run.ProcessRuns...)
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

func resolveInputs(step stepDef, outputs map[string]map[string]string, overrides map[string]string) (map[string]string, []string, error) {
	inputs := map[string]string{}
	var env []string
	for _, in := range step.Inputs {
		key := step.ID + "." + in.Name
		if ov, ok := overrides[key]; ok {
			inputs[in.Name] = ov
			env = append(env, fmt.Sprintf("SUBSPACE_INPUT_%s=%s", in.Name, ov))
			continue
		}
		if in.Source != nil {
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
			continue
		}
		if in.Value != "" {
			inputs[in.Name] = in.Value
			env = append(env, fmt.Sprintf("SUBSPACE_INPUT_%s=%s", in.Name, in.Value))
		}
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

func (e *Engine) runSeries(run *WorkflowRun, project string, step stepDef, env []string, argOverrides map[string][]string, jl *jsonl) error {
	for _, p := range step.Processes {
		pr, err := e.runProcess(project, p, env, argOverrides, jl, nil)
		run.ProcessRuns = append(run.ProcessRuns, pr)
		if err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) runParallel(run *WorkflowRun, project string, step stepDef, env []string, argOverrides map[string][]string, jl *jsonl) error {
	type slot struct {
		def  processDef
		cmd  *exec.Cmd
		term atomic.Bool
		pr   ProcessRun
	}
	slots := make([]*slot, len(step.Processes))
	for i, p := range step.Processes {
		cmd, err := e.startCmd(project, p, env, argOverrides, jl)
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

func (e *Engine) runDecision(run *WorkflowRun, project string, step stepDef, inputs map[string]string, env []string, argOverrides map[string][]string, jl *jsonl) error {
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
	pr, err := e.runProcess(project, matched[0], env, argOverrides, jl, nil)
	run.ProcessRuns = append(run.ProcessRuns, pr)
	return err
}

// runLoop runs Processes then returns steps[] delta: -1 previous, +1 next.
// when true → previous; when false/nil-match → next.
func (e *Engine) runLoop(run *WorkflowRun, project string, step stepDef, inputs map[string]string, env []string, argOverrides map[string][]string, jl *jsonl, idx int) (int, error) {
	if err := e.runSeries(run, project, step, env, argOverrides, jl); err != nil {
		return 0, err
	}
	back, err := predicateMatches(step.When, inputs)
	if err != nil {
		return 0, err
	}
	if back {
		if idx == 0 {
			return 0, fmt.Errorf("loop step %q: no previous step", step.ID)
		}
		_ = jl.Append(map[string]any{"type": "loop_branch", "stepId": step.ID, "direction": "previous"})
		return -1, nil
	}
	_ = jl.Append(map[string]any{"type": "loop_branch", "stepId": step.ID, "direction": "next"})
	return 1, nil
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

func (e *Engine) runProcess(project string, p processDef, env []string, argOverrides map[string][]string, jl *jsonl, termFlag *atomic.Bool) (ProcessRun, error) {
	cmd, err := e.startCmd(project, p, env, argOverrides, jl)
	if err != nil {
		return ProcessRun{ProcessID: p.ID, Status: StatusFailed}, err
	}
	return e.waitCmd(p, cmd, jl, termFlag)
}

func (e *Engine) startCmd(project string, p processDef, env []string, argOverrides map[string][]string, jl *jsonl) (*exec.Cmd, error) {
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
	now := time.Now().UTC()
	return fmt.Sprintf("%s-%04x", now.Format("20060102-150405"), now.Nanosecond()&0xffff)
}
