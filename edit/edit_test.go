package edit

import (
	"os"
	"path/filepath"
	"testing"
)

type doc []int

func (d doc) Clone() doc { return append(doc(nil), d...) }

func TestHistory(t *testing.T) {
	h := NewHistory(doc{1})
	h.Push(doc{1, 2})
	h.Push(doc{1, 2, 3})
	h.Amend(doc{1, 2, 4})
	if d, ok := h.Undo(); !ok || len(d) != 2 {
		t.Errorf("undo %v", d)
	}
	if d, ok := h.Redo(); !ok || d[2] != 4 {
		t.Errorf("redo %v, want the amended edit", d)
	}
	h.Undo()
	h.Push(doc{9})
	if h.CanRedo() {
		t.Error("an edit after undo left something to redo")
	}
	for range MaxHistory + 10 {
		h.Push(doc{0})
	}
	n := 0
	for h.CanUndo() {
		h.Undo()
		n++
	}
	if n != MaxHistory {
		t.Errorf("undid %d edits, want %d", n, MaxHistory)
	}
}

func TestApply(t *testing.T) {
	src := []byte("package p\n\nvar x = 1\n")
	out, err := Apply(src, []Splice{
		{From: 19, To: 20, Text: "2"},
		Insert(20, " + 3"),
		Insert(20, " + 4"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := string(out); got != "package p\n\nvar x = 2 + 3 + 4\n" {
		t.Errorf("got %q", got)
	}
	if _, err := Apply(src, []Splice{Insert(0, "{")}); err == nil {
		t.Error("source that doesn't parse was applied")
	}
}

func TestWriteFileKeepsTheMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "f.go")
	if err := os.WriteFile(path, []byte("a"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("b")); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	data, _ := os.ReadFile(path)
	if string(data) != "b" || info.Mode().Perm() != 0o640 {
		t.Errorf("wrote %q with mode %v", data, info.Mode())
	}
}
