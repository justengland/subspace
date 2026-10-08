package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/justengland/subspace/backend/api"
	"github.com/justengland/subspace/backend/engine"
	"github.com/justengland/subspace/backend/registry"
)

func TestHTTPListReposAfterRegister(t *testing.T) {
	home := t.TempDir()
	eng := engine.New(engine.Config{
		WorkflowsDir: filepath.Join(home, "workflows"),
		StorageRoot:  home,
	})
	h := (&api.Server{Eng: eng, Home: home}).Handler()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/repos", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("empty status %d: %s", rr.Code, rr.Body)
	}
	var empty []registry.Repo
	if err := json.Unmarshal(rr.Body.Bytes(), &empty); err != nil {
		t.Fatal(err)
	}
	if len(empty) != 0 {
		t.Fatalf("want empty list, got %+v", empty)
	}

	proj := filepath.Join(home, "myproj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := registry.Add(home, "demo", proj); err != nil {
		t.Fatal(err)
	}

	rr2 := httptest.NewRecorder()
	h.ServeHTTP(rr2, httptest.NewRequest(http.MethodGet, "/api/repos", nil))
	if rr2.Code != http.StatusOK {
		t.Fatalf("list status %d: %s", rr2.Code, rr2.Body)
	}
	var repos []registry.Repo
	if err := json.Unmarshal(rr2.Body.Bytes(), &repos); err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].Name != "demo" {
		t.Fatalf("repos=%+v", repos)
	}
	want, _ := filepath.Abs(proj)
	if repos[0].AbsolutePath != want {
		t.Fatalf("absolutePath=%q want %q", repos[0].AbsolutePath, want)
	}
}

// Empty Repo still appears via registry; nested lists are empty (home fan-out uses these).
func TestHTTPEmptyRepoListsViaNestedAPIs(t *testing.T) {
	home := t.TempDir()
	proj := filepath.Join(home, "empty-proj")
	if err := os.MkdirAll(proj, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := registry.Add(home, "empty", proj); err != nil {
		t.Fatal(err)
	}
	eng := engine.New(engine.Config{StorageRoot: home})
	h := (&api.Server{Eng: eng, Home: home}).Handler()

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/repos", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("repos %d: %s", rr.Code, rr.Body)
	}
	var repos []registry.Repo
	if err := json.Unmarshal(rr.Body.Bytes(), &repos); err != nil {
		t.Fatal(err)
	}
	if len(repos) != 1 || repos[0].Name != "empty" {
		t.Fatalf("repos=%+v", repos)
	}

	for _, path := range []string{"/api/workflows/empty", "/api/runs/empty"} {
		rrL := httptest.NewRecorder()
		h.ServeHTTP(rrL, httptest.NewRequest(http.MethodGet, path, nil))
		if rrL.Code != http.StatusOK {
			t.Fatalf("%s status %d: %s", path, rrL.Code, rrL.Body)
		}
		var list []json.RawMessage
		if err := json.Unmarshal(rrL.Body.Bytes(), &list); err != nil {
			t.Fatalf("%s json: %v", path, err)
		}
		if len(list) != 0 {
			t.Fatalf("%s want empty, got %s", path, rrL.Body)
		}
	}

	rrD := httptest.NewRecorder()
	h.ServeHTTP(rrD, httptest.NewRequest(http.MethodGet, "/api/directory", nil))
	if rrD.Code == http.StatusOK {
		t.Fatal("GET /api/directory must not exist")
	}
}
