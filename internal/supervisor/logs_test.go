package supervisor

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nietthijmen/bellhop/internal/api"
	"github.com/nietthijmen/bellhop/internal/events"
)

func TestRing(t *testing.T) {
	cases := []struct {
		size, add, tail int
		want            []string
	}{
		{size: 3, add: 0, tail: 0, want: nil},
		{size: 3, add: 2, tail: 0, want: []string{"0", "1"}},
		{size: 3, add: 5, tail: 0, want: []string{"2", "3", "4"}},
		{size: 3, add: 5, tail: 2, want: []string{"3", "4"}},
		{size: 3, add: 5, tail: 10, want: []string{"2", "3", "4"}},
		{size: 3, add: 7, tail: 1, want: []string{"6"}},
	}
	for _, tc := range cases {
		r := newRing(tc.size)
		for i := range tc.add {
			r.add(api.LogLine{Line: strconv.Itoa(i)})
		}
		var got []string
		for _, l := range r.tail(tc.tail) {
			got = append(got, l.Line)
		}
		if strings.Join(got, ",") != strings.Join(tc.want, ",") {
			t.Errorf("size %d add %d tail %d: got %v want %v", tc.size, tc.add, tc.tail, got, tc.want)
		}
	}
}

func TestRotatingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "svc.log")
	w := NewRotatingFile(path, 100, 3)
	line := strings.Repeat("x", 39) + "\n" // 40 bytes: two lines per file
	for i := range 10 {
		if _, err := w.Write([]byte(line)); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
		info, _ := e.Info()
		if info.Size() > 100 {
			t.Errorf("%s is %d bytes, over the limit", e.Name(), info.Size())
		}
	}
	if strings.Join(names, ",") != "svc.log,svc.log.1,svc.log.2" {
		t.Fatalf("files = %v", names)
	}

	// Reopening picks up the existing file's size: the full (80 byte) file
	// is rotated before the next 40 byte write.
	w = NewRotatingFile(path, 100, 3)
	if _, err := w.Write([]byte(line)); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	for p, want := range map[string]int{path: 40, path + ".1": 80, path + ".2": 80} {
		b, _ := os.ReadFile(p)
		if len(b) != want {
			t.Errorf("%s has %d bytes, want %d", filepath.Base(p), len(b), want)
		}
	}
}

func TestRotatingFileSingle(t *testing.T) {
	path := filepath.Join(t.TempDir(), "one.log")
	w := NewRotatingFile(path, 10, 1)
	defer func() { _ = w.Close() }()
	for _, s := range []string{"12345678\n", "abc\n"} {
		if _, err := w.Write([]byte(s)); err != nil {
			t.Fatal(err)
		}
	}
	_ = w.Close()
	b, _ := os.ReadFile(path)
	if string(b) != "abc\n" {
		t.Fatalf("content %q", b)
	}
}

func TestPumpSplitsLines(t *testing.T) {
	k := &sink{service: "t", pub: events.Discard, now: time.Now, ring: newRing(10)}
	long := strings.Repeat("y", maxLineBytes+10)
	k.pump(strings.NewReader("a\r\nb\n"+long+"\nlast"), StreamStdout)
	var got []int
	for _, l := range k.tail(0) {
		got = append(got, len(l.Line))
	}
	want := []int{1, 1, maxLineBytes, 10, 4}
	if len(got) != len(want) {
		t.Fatalf("line lengths %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line lengths %v, want %v", got, want)
		}
	}
}
