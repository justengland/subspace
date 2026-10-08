package api

import (
	"encoding/json"
	"net/http"

	"github.com/gorilla/websocket"
	"github.com/justengland/subspace/backend/engine"
)

type Server struct {
	Eng *engine.Engine
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(*http.Request) bool { return true },
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/runs", s.startRun)
	mux.HandleFunc("GET /api/runs", s.listRuns)
	mux.HandleFunc("GET /api/runs/{id}/events", s.followRun)
	mux.HandleFunc("GET /api/runs/{id}", s.getRun)
	mux.HandleFunc("POST /api/runs/{id}/pause", s.pauseRun)
	mux.HandleFunc("POST /api/runs/{id}/resume", s.resumeRun)
	mux.HandleFunc("POST /api/runs/{id}/stop", s.stopRun)
	mux.HandleFunc("POST /api/runs/{id}/rewind", s.rewindRun)
	mux.HandleFunc("GET /api/workflows/{id}", s.getWorkflow)
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

func (s *Server) listRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := s.Eng.List(r.URL.Query().Get("projectPath"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

func (s *Server) getRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
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

func (s *Server) rewindRun(w http.ResponseWriter, r *http.Request) {
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
	if err := s.Eng.Rewind(r.PathValue("id"), body.StepID); err != nil {
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
	id := r.PathValue("id")
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

func (s *Server) followRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
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
