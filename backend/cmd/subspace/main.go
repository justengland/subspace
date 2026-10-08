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
	"github.com/justengland/subspace/backend/registry"
)

func main() {
	if len(os.Args) >= 2 && os.Args[1] == "repo" {
		os.Exit(runRepo(os.Args[2:]))
	}

	addr := flag.String("addr", ":4201", "HTTP listen address")
	storage := flag.String("storage", "", "Subspace home / run storage root (default: SUBSPACE_HOME or ~/.local/subspace)")
	flag.Parse()

	store := *storage
	if store == "" {
		store = subspaceHome()
	}

	// Workflows load from StorageRoot/<repo>/workflows only (not monorepo ./workflows).
	eng := engine.New(engine.Config{StorageRoot: store})
	srv := &api.Server{Eng: eng, Home: store}
	fmt.Printf("subspace listening on %s (storage=%s)\n", *addr, store)
	log.Fatal(http.ListenAndServe(*addr, srv.Handler()))
}

func subspaceHome() string {
	if home := os.Getenv("SUBSPACE_HOME"); home != "" {
		return home
	}
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatal(err)
	}
	return filepath.Join(home, ".local", "subspace")
}

func runRepo(args []string) int {
	if len(args) < 1 || args[0] != "add" || len(args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: subspace repo add <name> <path>")
		return 2
	}
	if err := registry.Add(subspaceHome(), args[1], args[2]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
