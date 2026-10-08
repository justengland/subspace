package api

import (
	"encoding/json"
	"errors"
	"net/http"

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
	project, err := registry.AbsolutePath(s.Home, repo)
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
	run, err := s.Eng.StartInRepo(repo, project, req)
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
	if err := s.Eng.Pause(id); err != nil {
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
	if err := s.Eng.Resume(id); err != nil {
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
	if err := s.Eng.Stop(id); err != nil {
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
	if err := s.Eng.Rewind(id, body.StepID); err != nil {
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

func (s *Server) followRun(w http.ResponseWriter, r *http.Request) {
	_, id, ok := s.runPath(w, r)
	if !ok {
		return
	}
	ch, cancel, err := s.Eng.Follow(id)
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
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
