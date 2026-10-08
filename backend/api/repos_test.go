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
