package engine

import (
	"bufio"
	"encoding/json"
	"os"
	"sync"
	"time"
)

type jsonl struct {
	mu     sync.Mutex
	f      *os.File
	path   string
	closed bool
	subs   map[chan map[string]any]struct{}
}

func openJSONL(path string) (*jsonl, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	return &jsonl{f: f, path: path, subs: map[chan map[string]any]struct{}{}}, nil
}

func (j *jsonl) Append(ev map[string]any) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return os.ErrClosed
	}
	if _, ok := ev["ts"]; !ok {
		ev["ts"] = time.Now().UTC().Format(time.RFC3339Nano)
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if _, err := j.f.Write(b); err != nil {
		return err
	}
	for ch := range j.subs {
		select {
		case ch <- copyEvent(ev):
		default:
			// ponytail: drop if subscriber too slow; bump buffer if timeline gaps appear
		}
	}
	return nil
}

func (j *jsonl) Subscribe() (history []map[string]any, live <-chan map[string]any, cancel func()) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if !j.closed {
		_ = j.f.Sync()
	}
	history, _ = readJSONLFile(j.path)
	ch := make(chan map[string]any, 256)
	if j.closed {
		close(ch)
		return history, ch, func() {}
	}
	j.subs[ch] = struct{}{}
	var once sync.Once
	cancel = func() {
		once.Do(func() {
			j.mu.Lock()
			if _, ok := j.subs[ch]; ok {
				delete(j.subs, ch)
				close(ch)
			}
			j.mu.Unlock()
		})
	}
	return history, ch, cancel
}

func (j *jsonl) Close() error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.closed = true
	for ch := range j.subs {
		close(ch)
		delete(j.subs, ch)
	}
	return j.f.Close()
}

func readJSONLFile(path string) ([]map[string]any, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var ev map[string]any
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			continue
		}
		out = append(out, ev)
	}
	return out, sc.Err()
}

func copyEvent(ev map[string]any) map[string]any {
	cp := make(map[string]any, len(ev))
	for k, v := range ev {
		cp[k] = v
	}
	return cp
}
