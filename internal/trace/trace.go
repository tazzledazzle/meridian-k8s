// Package trace records which AWS operations a client (Karpenter) actually
// calls, so the supported-operation list is derived from evidence.
package trace

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// Recorder counts calls by "service:operation". It is safe for concurrent use.
type Recorder struct {
	mu          sync.Mutex // guards both maps (OnCall runs on many request goroutines)
	counts      map[string]int
	unsupported map[string]bool
}

// NewRecorder returns an empty recorder.
func NewRecorder() *Recorder {
	return &Recorder{counts: map[string]int{}, unsupported: map[string]bool{}}
}

// Record matches gateway.Config.OnCall.
func (r *Recorder) Record(service, op string, supported bool) {
	key := service + ":" + op
	r.mu.Lock()
	defer r.mu.Unlock()
	r.counts[key]++
	if !supported {
		r.unsupported[key] = true
	}
}

// Golden returns every distinct call, sorted (counts are omitted because they vary run to run).
func (r *Recorder) Golden() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return sortedKeys(r.counts)
}

// Unsupported returns the calls that are not in ops.yaml, sorted.
func (r *Recorder) Unsupported() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return sortedKeys(r.unsupported)
}

// JSON renders counts and the unsupported list for humans and scripts.
func (r *Recorder) JSON() ([]byte, error) {
	// One critical section, so counts and unsupported come from the same instant.
	r.mu.Lock()
	counts := make(map[string]int, len(r.counts))
	for k, v := range r.counts {
		counts[k] = v
	}
	unsupported := sortedKeys(r.unsupported)
	r.mu.Unlock()
	return json.MarshalIndent(struct {
		Calls       map[string]int `json:"calls"`
		Unsupported []string       `json:"unsupported"`
	}{counts, unsupported}, "", "  ")
}

// sortedKeys returns the map keys in order; never nil, so JSON renders [] not null.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Diff compares two golden call lists. Duplicates in either list are ignored.
func Diff(old, cur []string) (added, removed []string) {
	o, c := map[string]bool{}, map[string]bool{}
	for _, s := range old {
		o[s] = true
	}
	for _, s := range cur {
		c[s] = true
	}
	for s := range c {
		if !o[s] {
			added = append(added, s)
		}
	}
	for s := range o {
		if !c[s] {
			removed = append(removed, s)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	return added, removed
}

const goldenHeader = "# Karpenter AWS call surface (service:operation), sorted, counts omitted.\n" +
	"# Accept a new surface with `make golden-update` after reviewing the diff by hand.\n"

// WriteGolden writes a golden call list atomically (temp file, then rename), so
// a kill during the periodic flush never leaves a truncated file.
func WriteGolden(path string, lines []string) error {
	content := goldenHeader
	if len(lines) > 0 {
		content += strings.Join(lines, "\n") + "\n"
	}
	return writeFileAtomic(path, []byte(content))
}

// writeFileAtomic replaces path with data via a same-directory temp file.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil { // CreateTemp uses 0600
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// WriteFileAtomic is exported for callers (the gateway binary) that persist the JSON report.
func WriteFileAtomic(path string, data []byte) error { return writeFileAtomic(path, data) }

// ReadGolden reads a golden call list, skipping comments and blank lines.
func ReadGolden(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out, sc.Err()
}
