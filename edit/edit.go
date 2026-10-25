// Package edit is what editors of Go source share: an undo history of whole copies, splices
// of new source over old, and files written whole.
package edit

import (
	"errors"
	"go/ast"
	"go/format"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"sort"
)

// ErrChanged is a save refused because a file it writes changed on disk since it was
// loaded.
var ErrChanged = errors.New("changed on disk since it was loaded")

// MaxHistory is how many edits undo reaches back.
const MaxHistory = 200

// Doc is what a history holds a copy of after each edit.
type Doc[T any] interface {
	Clone() T
}

// History is the edits made to a doc, for undo and redo: a copy of the doc after each.
type History[T Doc[T]] struct {
	states []T
	at     int
}

func NewHistory[T Doc[T]](d T) *History[T] {
	return &History[T]{states: []T{d.Clone()}}
}

// Push records the doc after an edit, dropping anything undone before it.
func (h *History[T]) Push(d T) {
	h.states = append(h.states[:h.at+1], d.Clone())
	if len(h.states) > MaxHistory+1 {
		h.states = h.states[1:]
	}
	h.at = len(h.states) - 1
}

// Amend replaces the record of the last edit, for an edit that continues it, as typing
// into a field does.
func (h *History[T]) Amend(d T) {
	if h.at == 0 {
		h.Push(d)
		return
	}
	h.states = append(h.states[:h.at], d.Clone())
}

// Undo is the doc before the last edit, and false when there is nothing to undo.
func (h *History[T]) Undo() (T, bool) {
	if h.at == 0 {
		var zero T
		return zero, false
	}
	h.at--
	return h.states[h.at].Clone(), true
}

// Redo is the doc after the edit last undone, and false when there is nothing to redo.
func (h *History[T]) Redo() (T, bool) {
	if h.at == len(h.states)-1 {
		var zero T
		return zero, false
	}
	h.at++
	return h.states[h.at].Clone(), true
}

func (h *History[T]) CanUndo() bool {
	return h.at > 0
}

func (h *History[T]) CanRedo() bool {
	return h.at < len(h.states)-1
}

// Splice replaces the source from one offset to another with text.
type Splice struct {
	From, To int
	Text     string
}

// Apply makes the splices and formats the result. Splices are made from the end back, so
// each one's offsets are still the source's; an insertion at the end of a replaced range
// goes after it, and insertions at one place go in the order given.
func Apply(data []byte, splices []Splice) ([]byte, error) {
	if len(splices) == 0 {
		return data, nil
	}
	// The last given at a place is applied first.
	splices = slices.Clone(splices)
	slices.Reverse(splices)
	sort.SliceStable(splices, func(i, j int) bool {
		if splices[i].From != splices[j].From {
			return splices[i].From > splices[j].From
		}
		return splices[i].To > splices[j].To
	})
	out := slices.Clone(data)
	for _, s := range splices {
		out = slices.Concat(out[:s.From], []byte(s.Text), out[s.To:])
	}
	return format.Source(out)
}

// Offset is a position's offset in its file.
func Offset(fset *token.FileSet, p token.Pos) int {
	return fset.Position(p).Offset
}

// Replace is a splice of text over a node's source.
func Replace(fset *token.FileSet, n ast.Node, text string) Splice {
	return Splice{From: Offset(fset, n.Pos()), To: Offset(fset, n.End()), Text: text}
}

// Insert is a splice of text at an offset.
func Insert(at int, text string) Splice {
	return Splice{From: at, To: at, Text: text}
}

// WriteFile replaces a file by renaming a new one over it, so it is never half written.
func WriteFile(path string, data []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), info.Mode().Perm()); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// WriteNew writes a file that mustn't be there yet, so it is never half written.
func WriteNew(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	// A hard link fails, where a rename wouldn't, if the file appeared meanwhile.
	return os.Link(tmp.Name(), path)
}
