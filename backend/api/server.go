package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/gorilla/websocket"
	"github.com/justengland/subspace/backend/engine"
	"github.com/justengland/subspace/backend/registry"
)

type Server struct {
	Eng  *engine.Engine
	Home string // Subspace home; repos.json lives here
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(*http.Request) bool { return true },
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/runs/{repo}", s.startRun)
	mux.HandleFunc("GET /api/runs/{repo}", s.listRuns)
	mux.HandleFunc("GET /api/runs/{repo}/{runId}/events", s.followRun)
	mux.HandleFunc("POST /api/runs/{repo}/{runId}/pause", s.pauseRun)
	mux.HandleFunc("POST /api/runs/{repo}/{runId}/resume", s.resumeRun)
	mux.HandleFunc("POST /api/runs/{repo}/{runId}/stop", s.stopRun)
	mux.HandleFunc("POST /api/runs/{repo}/{runId}/rewind", s.rewindRun)
	mux.HandleFunc("GET /api/runs/{repo}/{runId}", s.getRun)
	mux.HandleFunc("GET /api/workflows/{repo}/{workflowId}", s.getWorkflow)
	mux.HandleFunc("PATCH /api/workflows/{repo}/{workflowId}", s.patchWorkflowVisualization)
	mux.HandleFunc("GET /api/workflows/{repo}", s.listWorkflows)
	mux.HandleFunc("GET /api/repos", s.listRepos)
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	return withCORS(mux)
}

func (s *Server) listRepos(w http.ResponseWriter, _ *http.Request) {
	repos, err := registry.List(s.Home)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, repos)
}

func (s *Server) startRun(w http.ResponseWriter, r *http.Request) {
	repo := r.PathValue("repo")
	if repo == "" {
		http.Error(w, "missing repo", http.StatusBadRequest)
		return
	}
	repoPath, err := registry.AbsolutePath(s.Home, repo)
	if err != nil {
		var nf *registry.NotFoundError
		if errors.As(err, &nf) {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var req engine.StartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.WorkflowID == "" {
		http.Error(w, "workflowId required", http.StatusBadRequest)
		return
	}
	run, err := s.Eng.StartInRepo(repo, repoPath, req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	repo := r.PathValue("repo")
	if repo == "" {
		http.Error(w, "missing repo", http.StatusBadRequest)
		return
	}
	if !s.requireRepo(w, repo) {
		return
	}
	runs, err := s.Eng.ListInRepo(repo)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	repo := r.PathValue("repo")
	id := r.PathValue("runId")
	if repo == "" || id == "" {
		http.Error(w, "missing repo or runId", http.StatusBadRequest)
		return
	}
	if !s.requireRepo(w, repo) {
		return
	}
	run, err := s.Eng.GetInRepo(repo, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) pauseRun(w http.ResponseWriter, r *http.Request) {
	repo, id, ok := s.runPath(w, r)
	if !ok {
		return
	}
	if err := s.Eng.PauseInRepo(repo, id); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.writeRunInRepo(w, repo, id)
}

func (s *Server) resumeRun(w http.ResponseWriter, r *http.Request) {
	repo, id, ok := s.runPath(w, r)
	if !ok {
		return
	}
	if err := s.Eng.ResumeInRepo(repo, id); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.writeRunInRepo(w, repo, id)
}

func (s *Server) stopRun(w http.ResponseWriter, r *http.Request) {
	repo, id, ok := s.runPath(w, r)
	if !ok {
		return
	}
	if err := s.Eng.StopInRepo(repo, id); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.writeRunInRepo(w, repo, id)
}

func (s *Server) rewindRun(w http.ResponseWriter, r *http.Request) {
	repo, id, ok := s.runPath(w, r)
	if !ok {
		return
	}
	var body struct {
		StepID string `json:"stepId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if body.StepID == "" {
		http.Error(w, "stepId required", http.StatusBadRequest)
		return
	}
	if err := s.Eng.RewindInRepo(repo, id, body.StepID); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.writeRunInRepo(w, repo, id)
}

func (s *Server) runPath(w http.ResponseWriter, r *http.Request) (repo, id string, ok bool) {
	repo = r.PathValue("repo")
	id = r.PathValue("runId")
	if repo == "" || id == "" {
		http.Error(w, "missing repo or runId", http.StatusBadRequest)
		return "", "", false
	}
	if !s.requireRepo(w, repo) {
		return "", "", false
	}
	if _, err := s.Eng.GetInRepo(repo, id); err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return "", "", false
	}
	return repo, id, true
}

func (s *Server) writeRunInRepo(w http.ResponseWriter, repo, id string) {
	run, err := s.Eng.GetInRepo(repo, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) requireRepo(w http.ResponseWriter, name string) bool {
	ok, err := registry.Has(s.Home, name)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return false
	}
	if !ok {
		http.Error(w, "unknown repo: "+name, http.StatusNotFound)
		return false
	}
	return true
}

func (s *Server) listWorkflows(w http.ResponseWriter, r *http.Request) {
	repo := r.PathValue("repo")
	if repo == "" {
		http.Error(w, "missing repo", http.StatusBadRequest)
		return
	}
	if !s.requireRepo(w, repo) {
		return
	}
	list, err := s.Eng.ListWorkflows(repo)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) getWorkflow(w http.ResponseWriter, r *http.Request) {
	repo := r.PathValue("repo")
	id := r.PathValue("workflowId")
	if repo == "" || id == "" {
		http.Error(w, "missing repo or workflowId", http.StatusBadRequest)
		return
	}
	if !s.requireRepo(w, repo) {
		return
	}
	wf, err := s.Eng.GetWorkflowInRepo(repo, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, wf)
}

func (s *Server) patchWorkflowVisualization(w http.ResponseWriter, r *http.Request) {
	repo := r.PathValue("repo")
	id := r.PathValue("workflowId")
	if repo == "" || id == "" {
		http.Error(w, "missing repo or workflowId", http.StatusBadRequest)
		return
	}
	if !s.requireRepo(w, repo) {
		return
	}
	var body struct {
		Steps     []engine.StepVizPatch `json:"steps"`
		Processes []engine.ProcessPatch `json:"processes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if len(body.Steps) == 0 && len(body.Processes) == 0 {
		http.Error(w, "steps or processes required", http.StatusBadRequest)
		return
	}
	var homePath string
	var err error
	msg := "Update workflow visualization"
	if len(body.Processes) > 0 {
		homePath, err = s.Eng.PatchProcessesInRepo(repo, id, body.Processes)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		msg = "Update workflow process"
	}
	if len(body.Steps) > 0 {
		homePath, err = s.Eng.PatchStepVisualizationsInRepo(repo, id, body.Steps)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				http.Error(w, err.Error(), http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	if err := commitWorkflowYAML(s.Home, repo, id, homePath, msg); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	wf, err := s.Eng.GetWorkflowInRepo(repo, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, wf)
}

// commitWorkflowYAML mirrors the Workflow YAML into the registered Repo working
// tree under workflows/<id>.yaml and git-commits it. No-op if AbsolutePath is
// not a git work tree.
func commitWorkflowYAML(home, repo, id, homePath, message string) error {
	abs, err := registry.AbsolutePath(home, repo)
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(abs, ".git")); err != nil {
		return nil // ponytail: skip commit when Repo is not a git checkout
	}
	data, err := os.ReadFile(homePath)
	if err != nil {
		return err
	}
	destDir := filepath.Join(abs, "workflows")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	dest := filepath.Join(destDir, id+".yaml")
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		return err
	}
	rel := filepath.Join("workflows", id+".yaml")
	add := exec.Command("git", "-C", abs, "add", "--", rel)
	if out, err := add.CombinedOutput(); err != nil {
		return fmt.Errorf("git add: %w: %s", err, out)
	}
	commit := exec.Command("git", "-C", abs, "commit", "-m", message, "--", rel)
	if out, err := commit.CombinedOutput(); err != nil {
		// nothing to commit is fine (identical content)
		if commit.ProcessState != nil && commit.ProcessState.ExitCode() == 1 {
			return nil
		}
		return fmt.Errorf("git commit: %w: %s", err, out)
	}
	return nil
}

func (s *Server) followRun(w http.ResponseWriter, r *http.Request) {
	repo, id, ok := s.runPath(w, r)
	if !ok {
		return
	}
	ch, cancel, err := s.Eng.FollowInRepo(repo, id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	defer cancel()

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	for ev := range ch {
		if err := conn.WriteJSON(ev); err != nil {
			return
		}
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PATCH, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
