package engine

import (
	"encoding/json"
	"os"
	"sync"
	"time"
)

type jsonl struct {
	mu sync.Mutex
	f  *os.File
}

func openJSONL(path string) (*jsonl, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	return &jsonl{f: f}, nil
}

func (j *jsonl) Append(ev map[string]any) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if _, ok := ev["ts"]; !ok {
		ev["ts"] = time.Now().UTC().Format(time.RFC3339Nano)
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = j.f.Write(b)
	return err
}

func (j *jsonl) Close() error {
	return j.f.Close()
}
