package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"

	"github.com/justengland/subspace/backend/api"
	"github.com/justengland/subspace/backend/engine"
)

func main() {
	addr := flag.String("addr", ":4201", "HTTP listen address")
	workflows := flag.String("workflows", "", "workflows directory (default: SUBSPACE_HOME/workflows or ./workflows)")
	storage := flag.String("storage", "", "run storage root (default: ~/.local/subspace)")
	flag.Parse()

	wfDir := *workflows
	if wfDir == "" {
		if home := os.Getenv("SUBSPACE_HOME"); home != "" {
			wfDir = filepath.Join(home, "workflows")
		} else {
			wd, _ := os.Getwd()
			wfDir = filepath.Join(wd, "workflows")
		}
	}
	store := *storage
	if store == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Fatal(err)
		}
		store = filepath.Join(home, ".local", "subspace")
	}

	eng := engine.New(engine.Config{WorkflowsDir: wfDir, StorageRoot: store})
	srv := &api.Server{Eng: eng}
	fmt.Printf("subspace listening on %s (workflows=%s storage=%s)\n", *addr, wfDir, store)
	log.Fatal(http.ListenAndServe(*addr, srv.Handler()))
}
