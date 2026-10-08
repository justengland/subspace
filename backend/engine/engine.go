package engine

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
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
	runs map[string]*runRec
}

type runRec struct {
	run  *WorkflowRun
	repo string // empty for legacy Start (WorkflowsDir); set for StartInRepo
	jl   *jsonl
	path string
	done chan struct{}

	mu       sync.Mutex
	pauseReq bool
	stopped  bool
	resumeCh chan struct{}
	cmds     []*exec.Cmd
	stepIDs  []string
	rewindTo int // -1 = none; index to resume from after Pause
}

type StartRequest struct {
	WorkflowID        string              `json:"workflowId"`
	ProjectPath       string              `json:"projectPath,omitempty"`      // empty → Workflow.defaultProject
	InputOverrides    map[string]string   `json:"inputOverrides,omitempty"`   // "stepId.inputName" → value
	ArgumentOverrides map[string][]string `json:"argumentOverrides,omitempty"` // processId → args
}

type ProcessRun struct {
	ProcessID string `json:"processId"`
	StepID    string `json:"stepId,omitempty"`
	Status    string `json:"status"`
	ExitCode  int    `json:"exitCode,omitempty"`
}

type StepRun struct {
	StepID    string `json:"stepId"`
	Status    string `json:"status"`
	Iteration int    `json:"iteration,omitempty"`
}

type WorkflowRun struct {
	ID           string       `json:"id"`
	WorkflowID   string       `json:"workflowId"`
	ProjectPath  string       `json:"projectPath"`
	Status       string       `json:"status"`
	CursorStepID string       `json:"cursorStepId,omitempty"`
	StepRuns     []StepRun    `json:"stepRuns,omitempty"`
	ProcessRuns  []ProcessRun `json:"processRuns,omitempty"`
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
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	Mode          string         `json:"mode"`
	Inputs        []InputView    `json:"inputs,omitempty"`
	Outputs       []OutputView   `json:"outputs,omitempty"`
	Visualization *Visualization `json:"visualization,omitempty"`
}

// Visualization is optional layout metadata (never affects execution).
type Visualization struct {
	Position *VizPosition `json:"position,omitempty"`
}

type VizPosition struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
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
	Visualization *vizDef       `yaml:"visualization"`
}

type vizDef struct {
	Position *vizPosDef `yaml:"position"`
}

type vizPosDef struct {
	X float64 `yaml:"x"`
	Y float64 `yaml:"y"`
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
	return e.beginRun("", wf, project, req)
}

// StartInRepo starts a Workflow from StorageRoot/<repo>/workflows and stores
// artifacts under StorageRoot/<repo>/<run-id>/. project is the Repo absolute path.
func (e *Engine) StartInRepo(repo, project string, req StartRequest) (*WorkflowRun, error) {
	wf, err := e.loadWorkflowInRepo(repo, req.WorkflowID)
	if err != nil {
		return nil, err
	}
	project, err = filepath.Abs(project)
	if err != nil {
		return nil, err
	}
	return e.beginRun(repo, wf, project, req)
}

func (e *Engine) beginRun(repo string, wf *workflowDef, project string, req StartRequest) (*WorkflowRun, error) {
	id := shortID()
	run := &WorkflowRun{
		ID:          id,
		WorkflowID:  wf.ID,
		ProjectPath: project,
		Status:      StatusRunning,
	}
	var runDir string
	if repo != "" {
		runDir = filepath.Join(e.cfg.StorageRoot, repo, id)
	} else {
		runDir = filepath.Join(e.cfg.StorageRoot, filepath.Base(project), id)
	}
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return nil, err
	}
	jsonlPath := filepath.Join(runDir, "events.jsonl")
	jl, err := openJSONL(jsonlPath)
	if err != nil {
		return nil, err
	}
	stepIDs := make([]string, len(wf.Steps))
	for i, s := range wf.Steps {
		stepIDs[i] = s.ID
	}
	rec := &runRec{
		run: run, repo: repo, jl: jl, path: jsonlPath, done: make(chan struct{}),
		resumeCh: make(chan struct{}), stepIDs: stepIDs, rewindTo: -1,
	}
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

	_ = jl.Append(map[string]any{"type": "workflow_started", "workflowId": wf.ID, "runId": run.ID, "projectPath": project})

	outputs := map[string]map[string]string{}
	loopIters := map[string]int{}

	for i := 0; i < len(wf.Steps); {
		if rec.isStopped() {
			rec.setStatus(StatusStopped)
			_ = jl.Append(map[string]any{"type": "workflow_stopped", "runId": run.ID})
			return
		}
		if err := rec.waitIfPaused(); err != nil {
			rec.setStatus(StatusStopped)
			_ = jl.Append(map[string]any{"type": "workflow_stopped", "runId": run.ID})
			return
		}
		if idx, ok := rec.takeRewind(); ok {
			i = idx
			for j := idx; j < len(wf.Steps); j++ {
				delete(outputs, wf.Steps[j].ID)
				delete(loopIters, wf.Steps[j].ID)
			}
			continue
		}

		step := wf.Steps[i]
		rec.setCursor(step.ID)
		mode := step.Mode
		if mode == "" {
			mode = "series"
		}
		inputs, env, err := resolveInputs(step, outputs, req.InputOverrides)
		if err != nil {
			rec.setStatus(StatusFailed)
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
				_ = jl.Append(map[string]any{"type": "step_started", "stepId": step.ID, "mode": mode, "iteration": iteration})
				failStep(rec, jl, step.ID, iteration, err)
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
			stepErr = e.runSeries(rec, project, step, env, req.ArgumentOverrides, jl)
			delta = 1
		case "parallel":
			stepErr = e.runParallel(rec, project, step, env, req.ArgumentOverrides, jl)
			delta = 1
		case "decision":
			stepErr = e.runDecision(rec, project, step, inputs, env, req.ArgumentOverrides, jl)
			delta = 1
		case "loop":
			delta, stepErr = e.runLoop(rec, project, step, inputs, env, req.ArgumentOverrides, jl, i)
		default:
			stepErr = fmt.Errorf("unsupported mode %q", mode)
		}
		if stepErr != nil {
			if rec.isStopped() {
				rec.setStatus(StatusStopped)
				_ = jl.Append(map[string]any{"type": "workflow_stopped", "runId": run.ID})
				return
			}
			failStep(rec, jl, step.ID, iteration, stepErr)
			return
		}
		stepOut := map[string]string{}
		for _, o := range step.Outputs {
			stepOut[o.Name] = o.Value
		}
		outputs[step.ID] = stepOut
		sev := map[string]any{"type": "step_succeeded", "stepId": step.ID}
		if iteration > 0 {
			sev["iteration"] = iteration
		}
		_ = jl.Append(sev)
		rec.appendStepRun(StepRun{StepID: step.ID, Status: StatusSucceeded, Iteration: iteration})
		i += delta
		if i < len(wf.Steps) {
			rec.setCursor(wf.Steps[i].ID)
		}

		if err := rec.waitIfPaused(); err != nil {
			rec.setStatus(StatusStopped)
			_ = jl.Append(map[string]any{"type": "workflow_stopped", "runId": run.ID})
			return
		}
		if idx, ok := rec.takeRewind(); ok {
			i = idx
			for j := idx; j < len(wf.Steps); j++ {
				delete(outputs, wf.Steps[j].ID)
				delete(loopIters, wf.Steps[j].ID)
			}
		}
	}

	rec.setStatus(StatusSucceeded)
	_ = jl.Append(map[string]any{"type": "workflow_succeeded", "runId": run.ID})
}

func (e *Engine) Get(id string) (*WorkflowRun, error) {
	e.mu.Lock()
	rec, ok := e.runs[id]
	e.mu.Unlock()
	if ok {
		rec.mu.Lock()
		cp := *rec.run
		cp.ProcessRuns = append([]ProcessRun(nil), rec.run.ProcessRuns...)
		cp.StepRuns = append([]StepRun(nil), rec.run.StepRuns...)
		rec.mu.Unlock()
		return &cp, nil
	}
	run, _, err := e.loadFromDisk(id)
	return run, err
}

// GetInRepo returns a WorkflowRun under StorageRoot/<repo>/<id>/.
func (e *Engine) GetInRepo(repo, id string) (*WorkflowRun, error) {
	e.mu.Lock()
	rec, ok := e.runs[id]
	e.mu.Unlock()
	if ok {
		if rec.repo != repo {
			return nil, fmt.Errorf("WorkflowRun %q not found in repo %q", id, repo)
		}
		rec.mu.Lock()
		cp := *rec.run
		cp.ProcessRuns = append([]ProcessRun(nil), rec.run.ProcessRuns...)
		cp.StepRuns = append([]StepRun(nil), rec.run.StepRuns...)
		rec.mu.Unlock()
		return &cp, nil
	}
	path := filepath.Join(e.cfg.StorageRoot, repo, id, "events.jsonl")
	events, err := readJSONLFile(path)
	if err != nil {
		return nil, fmt.Errorf("WorkflowRun %q not found in repo %q", id, repo)
	}
	run := reconstructRun(events)
	if run.ID == "" {
		run.ID = id
	}
	return run, nil
}

// List returns WorkflowRuns from memory and StorageRoot, newest id first.
// Optional projectPath filters by absolute Project path.
func (e *Engine) List(projectPath string) ([]WorkflowRun, error) {
	var abs string
	if projectPath != "" {
		var err error
		abs, err = filepath.Abs(projectPath)
		if err != nil {
			return nil, err
		}
	}
	seen := map[string]struct{}{}
	var out []WorkflowRun

	e.mu.Lock()
	for _, rec := range e.runs {
		rec.mu.Lock()
		if abs == "" || rec.run.ProjectPath == abs {
			cp := *rec.run
			cp.ProcessRuns = append([]ProcessRun(nil), rec.run.ProcessRuns...)
			cp.StepRuns = append([]StepRun(nil), rec.run.StepRuns...)
			out = append(out, cp)
			seen[cp.ID] = struct{}{}
		}
		rec.mu.Unlock()
	}
	e.mu.Unlock()

	disk, err := e.scanStorage(abs, seen)
	if err != nil {
		return nil, err
	}
	out = append(out, disk...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

// ListInRepo returns WorkflowRuns for one Repo (memory + StorageRoot/<repo>/), newest first.
func (e *Engine) ListInRepo(repo string) ([]WorkflowRun, error) {
	seen := map[string]struct{}{}
	var out []WorkflowRun

	e.mu.Lock()
	for _, rec := range e.runs {
		if rec.repo != repo {
			continue
		}
		rec.mu.Lock()
		cp := *rec.run
		cp.ProcessRuns = append([]ProcessRun(nil), rec.run.ProcessRuns...)
		cp.StepRuns = append([]StepRun(nil), rec.run.StepRuns...)
		out = append(out, cp)
		seen[cp.ID] = struct{}{}
		rec.mu.Unlock()
	}
	e.mu.Unlock()

	dir := filepath.Join(e.cfg.StorageRoot, repo)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
			return out, nil
		}
		return nil, err
	}
	for _, ent := range entries {
		if !ent.IsDir() || ent.Name() == "workflows" {
			continue
		}
		id := ent.Name()
		if _, ok := seen[id]; ok {
			continue
		}
		path := filepath.Join(dir, id, "events.jsonl")
		events, err := readJSONLFile(path)
		if err != nil || len(events) == 0 {
			continue
		}
		run := reconstructRun(events)
		if run.ID == "" {
			run.ID = id
		}
		out = append(out, *run)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out, nil
}

func (e *Engine) Rewind(id, stepID string) error {
	rec, err := e.getRec(id)
	if err != nil {
		return err
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.run.Status != StatusPaused {
		return fmt.Errorf("WorkflowRun %q is not paused", id)
	}
	idx := -1
	for i, s := range rec.stepIDs {
		if s == stepID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return fmt.Errorf("step %q not in workflow", stepID)
	}
	var keepSteps []StepRun
	for _, sr := range rec.run.StepRuns {
		if stepIndex(rec.stepIDs, sr.StepID) < idx {
			keepSteps = append(keepSteps, sr)
		}
	}
	rec.run.StepRuns = keepSteps
	var keepPR []ProcessRun
	for _, pr := range rec.run.ProcessRuns {
		if stepIndex(rec.stepIDs, pr.StepID) < idx {
			keepPR = append(keepPR, pr)
		}
	}
	rec.run.ProcessRuns = keepPR
	rec.run.CursorStepID = stepID
	rec.rewindTo = idx
	_ = rec.jl.Append(map[string]any{"type": "workflow_rewound", "runId": id, "stepId": stepID})
	return nil
}

func stepIndex(ids []string, id string) int {
	for i, s := range ids {
		if s == id {
			return i
		}
	}
	return -1
}

func (e *Engine) Pause(id string) error {
	rec, err := e.getRec(id)
	if err != nil {
		return err
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.stopped {
		return fmt.Errorf("WorkflowRun %q is stopped", id)
	}
	rec.pauseReq = true
	return nil
}

func (e *Engine) Resume(id string) error {
	rec, err := e.getRec(id)
	if err != nil {
		return err
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.run.Status != StatusPaused {
		return fmt.Errorf("WorkflowRun %q is not paused", id)
	}
	rec.pauseReq = false
	rec.run.Status = StatusRunning
	select {
	case rec.resumeCh <- struct{}{}:
	default:
	}
	return nil
}

func (e *Engine) Stop(id string) error {
	rec, err := e.getRec(id)
	if err != nil {
		return err
	}
	rec.mu.Lock()
	rec.stopped = true
	rec.pauseReq = false
	cmds := append([]*exec.Cmd(nil), rec.cmds...)
	if rec.run.Status == StatusPaused {
		select {
		case rec.resumeCh <- struct{}{}:
		default:
		}
	}
	rec.mu.Unlock()
	for _, cmd := range cmds {
		sigterm(cmd)
	}
	return nil
}

func (e *Engine) getRec(id string) (*runRec, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	rec, ok := e.runs[id]
	if !ok {
		return nil, fmt.Errorf("WorkflowRun %q not found", id)
	}
	return rec, nil
}

func (rec *runRec) setStatus(status string) {
	rec.mu.Lock()
	rec.run.Status = status
	rec.mu.Unlock()
}

func (rec *runRec) setCursor(stepID string) {
	rec.mu.Lock()
	rec.run.CursorStepID = stepID
	rec.mu.Unlock()
}

func (rec *runRec) appendStepRun(sr StepRun) {
	rec.mu.Lock()
	rec.run.StepRuns = append(rec.run.StepRuns, sr)
	rec.mu.Unlock()
}

func (rec *runRec) takeRewind() (int, bool) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.rewindTo < 0 {
		return 0, false
	}
	idx := rec.rewindTo
	rec.rewindTo = -1
	return idx, true
}

func (rec *runRec) isStopped() bool {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return rec.stopped
}

func (rec *runRec) waitIfPaused() error {
	rec.mu.Lock()
	if rec.stopped {
		rec.mu.Unlock()
		return fmt.Errorf("stopped")
	}
	if !rec.pauseReq {
		rec.mu.Unlock()
		return nil
	}
	rec.run.Status = StatusPaused
	ch := rec.resumeCh
	rec.mu.Unlock()
	<-ch
	rec.mu.Lock()
	stopped := rec.stopped
	if !stopped {
		rec.run.Status = StatusRunning
	}
	rec.mu.Unlock()
	if stopped {
		return fmt.Errorf("stopped")
	}
	return nil
}

func (rec *runRec) addCmd(cmd *exec.Cmd) {
	rec.mu.Lock()
	rec.cmds = append(rec.cmds, cmd)
	rec.mu.Unlock()
}

func (rec *runRec) removeCmd(cmd *exec.Cmd) {
	rec.mu.Lock()
	for i, c := range rec.cmds {
		if c == cmd {
			rec.cmds = append(rec.cmds[:i], rec.cmds[i+1:]...)
			break
		}
	}
	rec.mu.Unlock()
}

func (rec *runRec) appendProcessRun(pr ProcessRun) {
	rec.mu.Lock()
	rec.run.ProcessRuns = append(rec.run.ProcessRuns, pr)
	rec.mu.Unlock()
}

func sigterm(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
}

// Follow sends JSONL history then live appends (same map shape as the file).
// Cold (disk-only) runs stream history then close.
func (e *Engine) Follow(id string) (<-chan map[string]any, func(), error) {
	e.mu.Lock()
	rec, ok := e.runs[id]
	e.mu.Unlock()

	out := make(chan map[string]any, 256)
	stop := make(chan struct{})
	var once sync.Once
	cancel := func() { once.Do(func() { close(stop) }) }

	if !ok {
		_, path, err := e.loadFromDisk(id)
		if err != nil {
			return nil, nil, err
		}
		hist, err := readJSONLFile(path)
		if err != nil {
			return nil, nil, err
		}
		go func() {
			defer close(out)
			for _, ev := range hist {
				select {
				case out <- ev:
				case <-stop:
					return
				}
			}
		}()
		return out, cancel, nil
	}

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

// ListWorkflows returns Workflows under StorageRoot/<repo>/workflows/*.yaml.
func (e *Engine) ListWorkflows(repo string) ([]Workflow, error) {
	dir := filepath.Join(e.cfg.StorageRoot, repo, "workflows")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []Workflow{}, nil
		}
		return nil, err
	}
	out := []Workflow{}
	for _, ent := range entries {
		name := ent.Name()
		if ent.IsDir() || filepath.Ext(name) != ".yaml" {
			continue
		}
		id := strings.TrimSuffix(name, ".yaml")
		wf, err := e.loadWorkflowInRepo(repo, id)
		if err != nil {
			return nil, err
		}
		out = append(out, *toWorkflowView(wf))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// GetWorkflowInRepo loads one Workflow from StorageRoot/<repo>/workflows/<id>.yaml.
func (e *Engine) GetWorkflowInRepo(repo, id string) (*Workflow, error) {
	wf, err := e.loadWorkflowInRepo(repo, id)
	if err != nil {
		return nil, err
	}
	return toWorkflowView(wf), nil
}

func (e *Engine) loadWorkflowInRepo(repo, id string) (*workflowDef, error) {
	path := filepath.Join(e.cfg.StorageRoot, repo, "workflows", id+".yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("load workflow %q/%q: %w", repo, id, err)
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

func toWorkflowView(wf *workflowDef) *Workflow {
	out := &Workflow{ID: wf.ID, Name: wf.Name, DefaultProject: wf.DefaultProject}
	for _, s := range wf.Steps {
		sv := StepView{ID: s.ID, Name: s.Name, Mode: s.Mode}
		if s.Visualization != nil && s.Visualization.Position != nil {
			sv.Visualization = &Visualization{
				Position: &VizPosition{X: s.Visualization.Position.X, Y: s.Visualization.Position.Y},
			}
		}
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

func (e *Engine) runSeries(rec *runRec, project string, step stepDef, env []string, argOverrides map[string][]string, jl *jsonl) error {
	for _, p := range step.Processes {
		if rec.isStopped() {
			return fmt.Errorf("stopped")
		}
		pr, err := e.runProcess(rec, project, p, env, argOverrides, jl, nil)
		pr.StepID = step.ID
		rec.appendProcessRun(pr)
		if err != nil {
			return err
		}
	}
	return nil
}

func (e *Engine) runParallel(rec *runRec, project string, step stepDef, env []string, argOverrides map[string][]string, jl *jsonl) error {
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
		rec.addCmd(cmd)
		if rec.isStopped() {
			sigterm(cmd)
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
			rec.removeCmd(s.cmd)
			if rec.isStopped() && pr.Status != StatusStopped {
				pr.Status = StatusStopped
				err = fmt.Errorf("stopped")
			}
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
			if err != nil && firstErr == nil && rec.isStopped() {
				firstErr = err
			}
			mu.Unlock()
		}(s)
	}
	wg.Wait()

	for _, s := range slots {
		s.pr.StepID = step.ID
		rec.appendProcessRun(s.pr)
	}
	return firstErr
}

func (e *Engine) runDecision(rec *runRec, project string, step stepDef, inputs map[string]string, env []string, argOverrides map[string][]string, jl *jsonl) error {
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
	pr, err := e.runProcess(rec, project, matched[0], env, argOverrides, jl, nil)
	pr.StepID = step.ID
	rec.appendProcessRun(pr)
	return err
}

// runLoop runs Processes then returns steps[] delta: -1 previous, +1 next.
// when true → previous; when false/nil-match → next.
func (e *Engine) runLoop(rec *runRec, project string, step stepDef, inputs map[string]string, env []string, argOverrides map[string][]string, jl *jsonl, idx int) (int, error) {
	if err := e.runSeries(rec, project, step, env, argOverrides, jl); err != nil {
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
		_ = jl.Append(map[string]any{"type": "loop_continue", "stepId": step.ID, "direction": "previous"})
		return -1, nil
	}
	_ = jl.Append(map[string]any{"type": "loop_continue", "stepId": step.ID, "direction": "next"})
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

func (e *Engine) runProcess(rec *runRec, project string, p processDef, env []string, argOverrides map[string][]string, jl *jsonl, termFlag *atomic.Bool) (ProcessRun, error) {
	cmd, err := e.startCmd(project, p, env, argOverrides, jl)
	if err != nil {
		return ProcessRun{ProcessID: p.ID, Status: StatusFailed}, err
	}
	rec.addCmd(cmd)
	if rec.isStopped() {
		sigterm(cmd)
	}
	pr, err := e.waitCmd(p, cmd, jl, termFlag)
	rec.removeCmd(cmd)
	if rec.isStopped() {
		if pr.Status != StatusStopped {
			_ = jl.Append(map[string]any{"type": "process_stopped", "processId": p.ID, "exitCode": pr.ExitCode})
			pr.Status = StatusStopped
		}
		return pr, fmt.Errorf("stopped")
	}
	return pr, err
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

func failStep(rec *runRec, jl *jsonl, stepID string, iteration int, err error) {
	sr := StepRun{StepID: stepID, Status: StatusFailed, Iteration: iteration}
	rec.appendStepRun(sr)
	ev := map[string]any{"type": "step_failed", "stepId": stepID, "error": err.Error()}
	if iteration > 0 {
		ev["iteration"] = iteration
	}
	_ = jl.Append(ev)
	rec.setStatus(StatusFailed)
	_ = jl.Append(map[string]any{"type": "workflow_failed", "error": err.Error()})
}

func (e *Engine) scanStorage(absProject string, skip map[string]struct{}) ([]WorkflowRun, error) {
	if e.cfg.StorageRoot == "" {
		return nil, nil
	}
	var out []WorkflowRun
	err := filepath.WalkDir(e.cfg.StorageRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Name() != "events.jsonl" {
			return nil
		}
		runID := filepath.Base(filepath.Dir(path))
		if _, ok := skip[runID]; ok {
			return nil
		}
		events, err := readJSONLFile(path)
		if err != nil || len(events) == 0 {
			return nil
		}
		run := reconstructRun(events)
		if run.ID == "" {
			run.ID = runID
		}
		if absProject != "" && run.ProjectPath != absProject {
			return nil
		}
		out = append(out, *run)
		return nil
	})
	return out, err
}

func (e *Engine) loadFromDisk(id string) (*WorkflowRun, string, error) {
	if e.cfg.StorageRoot == "" {
		return nil, "", fmt.Errorf("WorkflowRun %q not found", id)
	}
	var found string
	_ = filepath.WalkDir(e.cfg.StorageRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		if d.Name() == "events.jsonl" && filepath.Base(filepath.Dir(path)) == id {
			found = path
			return filepath.SkipAll
		}
		return nil
	})
	if found == "" {
		return nil, "", fmt.Errorf("WorkflowRun %q not found", id)
	}
	events, err := readJSONLFile(found)
	if err != nil {
		return nil, "", err
	}
	run := reconstructRun(events)
	if run.ID == "" {
		run.ID = id
	}
	return run, found, nil
}

func reconstructRun(events []map[string]any) *WorkflowRun {
	run := &WorkflowRun{Status: StatusRunning}
	for _, ev := range events {
		typ, _ := ev["type"].(string)
		switch typ {
		case "workflow_started":
			run.ID = eventStr(ev, "runId")
			run.WorkflowID = eventStr(ev, "workflowId")
			run.ProjectPath = eventStr(ev, "projectPath")
			run.Status = StatusRunning
		case "step_started":
			run.CursorStepID = eventStr(ev, "stepId")
		case "step_succeeded":
			run.StepRuns = append(run.StepRuns, StepRun{
				StepID: eventStr(ev, "stepId"), Status: StatusSucceeded, Iteration: eventInt(ev, "iteration"),
			})
		case "step_failed":
			run.StepRuns = append(run.StepRuns, StepRun{
				StepID: eventStr(ev, "stepId"), Status: StatusFailed, Iteration: eventInt(ev, "iteration"),
			})
		case "process_succeeded":
			run.ProcessRuns = append(run.ProcessRuns, ProcessRun{
				ProcessID: eventStr(ev, "processId"), Status: StatusSucceeded, ExitCode: eventInt(ev, "exitCode"),
			})
		case "process_failed":
			run.ProcessRuns = append(run.ProcessRuns, ProcessRun{
				ProcessID: eventStr(ev, "processId"), Status: StatusFailed, ExitCode: eventInt(ev, "exitCode"),
			})
		case "process_stopped":
			run.ProcessRuns = append(run.ProcessRuns, ProcessRun{
				ProcessID: eventStr(ev, "processId"), Status: StatusStopped, ExitCode: eventInt(ev, "exitCode"),
			})
		case "workflow_succeeded":
			run.Status = StatusSucceeded
		case "workflow_failed":
			run.Status = StatusFailed
		case "workflow_stopped":
			run.Status = StatusStopped
		case "workflow_paused":
			run.Status = StatusPaused
		}
	}
	return run
}

func eventStr(ev map[string]any, key string) string {
	if v, ok := ev[key].(string); ok {
		return v
	}
	return ""
}

func eventInt(ev map[string]any, key string) int {
	switch v := ev[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	default:
		return 0
	}
}
