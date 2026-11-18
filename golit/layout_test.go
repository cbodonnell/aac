package golit

import (
	"slices"
	"strings"
	"testing"
)

type Bin struct {
	Name string
	Size int
	Lots []Lot
	Runs []Lot
	Mark bool
}

type Lot struct {
	Label string
	N     int
	Lines []string
}

const binSrc = `package golit

// bin is planted source: a list closing on its one element's line, and a list in rows.
var bin = Bin{
	Name: "a",
	Lots: []Lot{{Label: "x", Lines: []string{
		"one",
		"two",
	}}},
	Runs: []Lot{
		{Label: "r1"}, {Label: "r2"},
		{Label: "r3"}, {Label: "r4"},
	},
}
`

func cloneBin(b Bin) Bin {
	c := b
	c.Lots, c.Runs = slices.Clone(b.Lots), slices.Clone(b.Runs)
	return c
}

// writeAndUndo writes an edit to the bin, checks the source has each want, and writes the
// bin back as it was, which must be the source again.
func writeAndUndo(t *testing.T, sp Spellings, edit func(*Bin), want ...string) {
	t.Helper()
	l, err := Read([]byte(binSrc), "bin", Bin{}, sp)
	if err != nil {
		t.Fatal(err)
	}
	now := cloneBin(l.Value)
	edit(&now)
	out, back, err := l.Write(now, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range want {
		if !strings.Contains(string(out), w) {
			t.Errorf("written source has no %q:\n%s", w, out)
		}
	}
	again, _, err := back.Write(l.Value, nil, nil)
	if err != nil || string(again) != binSrc {
		t.Errorf("written and undone, the source isn't as it was: %v\n%s", err, again)
	}
}

// What is added to a literal closing on its last element's line goes inline, so it still
// closes there.
func TestWriteHuggingLiterals(t *testing.T) {
	writeAndUndo(t, Spellings{}, func(b *Bin) { b.Lots = append(b.Lots, Lot{Label: "y"}) },
		"\t}}, {Label: \"y\"}},\n")
	writeAndUndo(t, Spellings{}, func(b *Bin) { b.Lots[0].N = 3 },
		`Lots: []Lot{{Label: "x", N: 3, Lines: []string{`)
}

// What is added to a list written several to a line goes into the row it falls in.
func TestWriteRows(t *testing.T) {
	writeAndUndo(t, Spellings{}, func(b *Bin) { b.Runs = append(b.Runs, Lot{Label: "r5"}) },
		`{Label: "r3"}, {Label: "r4"}, {Label: "r5"},`)
	writeAndUndo(t, Spellings{}, func(b *Bin) { b.Runs = slices.Insert(b.Runs, 2, Lot{Label: "new"}) },
		`{Label: "new"}, {Label: "r3"}, {Label: "r4"},`)
}

// A field added to a literal spanning lines goes before a list closing it, or with
// Declared in the order declared.
func TestWriteDeclared(t *testing.T) {
	size := func(b *Bin) { b.Size = 3 }
	mark := func(b *Bin) { b.Mark = true }
	writeAndUndo(t, Spellings{}, size, "Size: 3,\n\tRuns: []Lot{")
	writeAndUndo(t, Spellings{}, mark, "Mark: true,\n\tRuns: []Lot{")
	writeAndUndo(t, Spellings{Declared: true}, size, "Name: \"a\",\n\tSize: 3,\n\tLots:")
	writeAndUndo(t, Spellings{Declared: true}, mark, "\t},\n\tMark: true,\n}")
}

// A new list of composite values goes one a line, or with Inline on one line when it fits;
// strings go one a line either way.
func TestWriteInline(t *testing.T) {
	v := Bin{Runs: []Lot{{Label: "a"}, {Label: "b", Lines: []string{"x", "y"}}}}
	text, _, err := Format(v, "golit", Spellings{}, nil)
	if err != nil || !strings.Contains(text, "Runs: []Lot{\n") {
		t.Errorf("formatted %s: %v", text, err)
	}
	v.Runs[1].Lines = nil
	text, _, err = Format(v, "golit", Spellings{Inline: true}, nil)
	if err != nil || !strings.Contains(text, `Runs: []Lot{{Label: "a"}, {Label: "b"}}`) {
		t.Errorf("formatted with Inline %s: %v", text, err)
	}
	v.Runs[1].Lines = []string{"x", "y"}
	text, _, err = Format(v, "golit", Spellings{Inline: true}, nil)
	if err != nil || !strings.Contains(text, "Lines: []string{\n") {
		t.Errorf("formatted with Inline, strings inline: %s: %v", text, err)
	}
}

// A new struct spanning lines for the list it ends with closes on that list's last line
// with Hug.
func TestWriteHug(t *testing.T) {
	v := Lot{Label: "a", N: 3, Lines: []string{"x", "y"}}
	text, _, err := Format(v, "golit", Spellings{}, nil)
	if err != nil || !strings.HasPrefix(text, "Lot{\n") {
		t.Errorf("formatted %s: %v", text, err)
	}
	text, _, err = Format(v, "golit", Spellings{Hug: true}, nil)
	if err != nil || !strings.Contains(text, `Lot{Label: "a", N: 3, Lines: []string{`+"\n") || !strings.HasSuffix(text, "}}") {
		t.Errorf("formatted with Hug %s: %v", text, err)
	}
}
