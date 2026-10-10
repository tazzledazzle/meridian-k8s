package trace

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func TestRecorderGoldenIsSortedAndDeduplicated(t *testing.T) {
	r := NewRecorder()
	r.Record("ec2", "CreateFleet", true)
	r.Record("ec2", "CreateFleet", true)
	r.Record("iam", "GetInstanceProfile", true)
	r.Record("ec2", "DescribeCapacityReservations", false)
	want := []string{"ec2:CreateFleet", "ec2:DescribeCapacityReservations", "iam:GetInstanceProfile"}
	if got := r.Golden(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Golden() = %v, want %v", got, want)
	}
	if got := r.Unsupported(); !reflect.DeepEqual(got, []string{"ec2:DescribeCapacityReservations"}) {
		t.Fatalf("Unsupported() = %v", got)
	}
}

func TestRecorderJSONIncludesCountsAndUnsupported(t *testing.T) {
	r := NewRecorder()
	r.Record("ec2", "CreateFleet", true)
	r.Record("ec2", "CreateFleet", true)
	b, err := r.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Calls       map[string]int `json:"calls"`
		Unsupported []string       `json:"unsupported"`
	}
	if err := json.Unmarshal(b, &out); err != nil || out.Calls["ec2:CreateFleet"] != 2 {
		t.Fatalf("out=%+v err=%v", out, err)
	}
}

// An empty recorder must still render "unsupported" as a list, not null, so scripts can iterate it.
func TestRecorderJSONRendersEmptyListsNotNull(t *testing.T) {
	b, err := NewRecorder().JSON()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"unsupported": []`) || !strings.Contains(string(b), `"calls": {}`) {
		t.Fatalf("json = %s", b)
	}
}

func TestRecorderIsSafeForConcurrentUse(t *testing.T) {
	r := NewRecorder()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.Record("ec2", "DescribeInstances", true)
			_ = r.Golden() // readers run alongside writers under -race
			_, _ = r.JSON()
		}()
	}
	wg.Wait()
	b, _ := r.JSON()
	var out struct{ Calls map[string]int }
	_ = json.Unmarshal(b, &out)
	if out.Calls["ec2:DescribeInstances"] != 50 {
		t.Fatalf("count = %d, want 50", out.Calls["ec2:DescribeInstances"])
	}
}

func TestDiffReportsAddedAndRemoved(t *testing.T) {
	added, removed := Diff([]string{"a:1", "a:2"}, []string{"a:2", "a:3"})
	if !reflect.DeepEqual(added, []string{"a:3"}) || !reflect.DeepEqual(removed, []string{"a:1"}) {
		t.Fatalf("added=%v removed=%v", added, removed)
	}
}

func TestGoldenFileRoundTripSkipsComments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "calls.golden")
	lines := []string{"ec2:CreateFleet", "iam:GetInstanceProfile"}
	if err := WriteGolden(path, lines); err != nil {
		t.Fatal(err)
	}
	got, err := ReadGolden(path)
	if err != nil || !reflect.DeepEqual(got, lines) {
		t.Fatalf("got=%v err=%v", got, err)
	}
}

func TestGoldenEmptyListRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "calls.golden")
	if err := WriteGolden(path, nil); err != nil {
		t.Fatal(err)
	}
	got, err := ReadGolden(path)
	if err != nil || len(got) != 0 {
		t.Fatalf("got=%v err=%v", got, err)
	}
}

// The gateway flushes every 10s and a container can be killed mid-write, so the
// write must be atomic: no temp files left behind and the old file replaced whole.
func TestWriteGoldenReplacesAtomicallyWithoutLeftovers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "calls.golden")
	if err := WriteGolden(path, []string{"a:1"}); err != nil {
		t.Fatal(err)
	}
	if err := WriteGolden(path, []string{"a:2"}); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("directory has %d entries, want only calls.golden", len(entries))
	}
	if got, _ := ReadGolden(path); !reflect.DeepEqual(got, []string{"a:2"}) {
		t.Fatalf("got %v", got)
	}
}

func TestReadGoldenMissingFileIsAnError(t *testing.T) {
	if _, err := ReadGolden(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Fatal("expected an error")
	}
}

func TestDiffIgnoresDuplicates(t *testing.T) {
	added, removed := Diff([]string{"a:1", "a:1"}, []string{"a:2", "a:2"})
	if !reflect.DeepEqual(added, []string{"a:2"}) || !reflect.DeepEqual(removed, []string{"a:1"}) {
		t.Fatalf("added=%v removed=%v", added, removed)
	}
}
