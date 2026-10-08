package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/justengland/subspace/backend/engine"
)

type Server struct {
	Eng *engine.Engine
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/runs", s.startRun)
	mux.HandleFunc("GET /api/runs/{id}", s.getRun)
	mux.HandleFunc("POST /api/runs/{id}/pause", s.pauseRun)
	mux.HandleFunc("POST /api/runs/{id}/resume", s.resumeRun)
	mux.HandleFunc("POST /api/runs/{id}/stop", s.stopRun)
	mux.HandleFunc("GET /api/workflows/", s.getWorkflow)
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	return withCORS(mux)
}

func (s *Server) startRun(w http.ResponseWriter, r *http.Request) {
	var req engine.StartRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	run, err := s.Eng.Start(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		id = strings.TrimPrefix(r.URL.Path, "/api/runs/")
	}
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	run, err := s.Eng.Get(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) pauseRun(w http.ResponseWriter, r *http.Request) {
	if err := s.Eng.Pause(r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.writeRun(w, r.PathValue("id"))
}

func (s *Server) resumeRun(w http.ResponseWriter, r *http.Request) {
	if err := s.Eng.Resume(r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.writeRun(w, r.PathValue("id"))
}

func (s *Server) stopRun(w http.ResponseWriter, r *http.Request) {
	if err := s.Eng.Stop(r.PathValue("id")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.writeRun(w, r.PathValue("id"))
}

func (s *Server) writeRun(w http.ResponseWriter, id string) {
	run, err := s.Eng.Get(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) getWorkflow(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, "/api/workflows/")
	if id == "" {
		http.Error(w, "missing id", http.StatusBadRequest)
		return
	}
	wf, err := s.Eng.GetWorkflow(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, wf)
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
