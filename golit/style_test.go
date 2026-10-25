package golit

import (
	"go/ast"
	"go/format"
	"image"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
)

type Tone uint32

func tone(hex uint32) Tone { return Tone(hex) }

type Strip struct {
	Size   image.Point
	Keys   map[rune]Key
	Sets   map[string]map[string]int
	Order  []int
	Fade   []float64
	Moves  []image.Point
	Tones  []Tone
	Frames [][]string
}

const stripSrc = `package golit

import "image"

// strip is planted source.
var strip = Strip{
	Size: image.Pt(4, 4),
	Keys: map[rune]Key{
		'o': {"ink", 0},
	},
	Sets: map[string]map[string]int{
		"a": {"ink": one, "skin": 2},
	},
	Order: []int{0, 0},
	Moves: []image.Point{{0, 0}, {1, 0}},
	Tones: []Tone{tone(0x00ff80)},
	Frames: [][]string{
		{
			"o.",
			".o",
		},
	},
}

var other = []int{1, 2}
`

func stripSpellings() Spellings {
	return Spellings{
		Vars:    map[string]any{"one": 1},
		Helpers: map[string]Helper{"tone": {Func: tone, Label: "tone", Args: []string{"hex"}, Hex: 6}},
	}
}

func stripFile(t *testing.T) []byte {
	t.Helper()
	src, err := format.Source([]byte(stripSrc))
	if err != nil {
		t.Fatal(err)
	}
	return src
}

func cloneStrip(s Strip) Strip {
	c := s
	c.Keys = maps.Clone(s.Keys)
	c.Sets = map[string]map[string]int{}
	for k, v := range s.Sets {
		c.Sets[k] = maps.Clone(v)
	}
	c.Order, c.Fade, c.Moves, c.Tones = slices.Clone(s.Order), slices.Clone(s.Fade), slices.Clone(s.Moves), slices.Clone(s.Tones)
	c.Frames = slices.Clone(s.Frames)
	return c
}

// Elements added take their siblings' style: structs by position, maps inline, points
// braced, helper calls with their arguments in hex; and a list of calls is inline.
func TestWriteSiblingsStyle(t *testing.T) {
	src := stripFile(t)
	l, err := Read(src, "strip", Strip{}, stripSpellings())
	if err != nil {
		t.Fatal(err)
	}
	now := cloneStrip(l.Value)
	now.Keys['x'] = Key{"skin", 1}
	now.Sets["b"] = map[string]int{"ink": 1, "skin": 3}
	now.Moves = append(now.Moves, image.Pt(0, -1))
	now.Tones = append(now.Tones, tone(0x0000ff))
	spelled := map[string]Spelling{
		`Sets["b"]["ink"]`: {Kind: Var, Name: "one"},
		"Tones[1]":         {Kind: Call, Name: "tone", Args: []any{uint32(0xff)}},
	}
	out, back, err := l.Write(now, spelled, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`'x': {"skin", 1},`,
		`"b": {"ink": one, "skin": 3},`,
		"Moves: []image.Point{{0, 0}, {1, 0}, {0, -1}},",
		"Tones: []Tone{tone(0x00ff80), tone(0x0000ff)},",
	} {
		if !strings.Contains(string(out), want) {
			t.Errorf("written source has no %q:\n%s", want, out)
		}
	}
	again, _, err := back.Write(l.Value, nil, nil)
	if err != nil || string(again) != string(src) {
		t.Errorf("written and undone, the source isn't as it was: %v\n%s", err, again)
	}
	text, _, err := Format([]Tone{tone(1), tone(2)}, "golit", stripSpellings(), map[string]Spelling{
		"[0]": {Kind: Call, Name: "tone", Args: []any{uint32(1)}},
		"[1]": {Kind: Call, Name: "tone", Args: []any{uint32(2)}},
	})
	if err != nil || text != "[]Tone{tone(0x000001), tone(0x000002)}" {
		t.Errorf("formatted %s: %v", text, err)
	}
}

// A field added to a literal on one line goes in the order declared, and one on a line of
// its own before a list closing the literal; taken out again, the source is as it was.
func TestWriteFieldPlacement(t *testing.T) {
	src := stripFile(t)
	l, err := Read(src, "strip", Strip{}, stripSpellings())
	if err != nil {
		t.Fatal(err)
	}
	now := cloneStrip(l.Value)
	now.Fade = []float64{1, 0.5}
	out, back, err := l.Write(now, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := "Tones: []Tone{tone(0x00ff80)},\n\tFade:  []float64{1, 0.5},\n\tFrames: [][]string{"; !strings.Contains(string(out), want) {
		t.Errorf("written source has no %q:\n%s", want, out)
	}
	again, _, err := back.Write(l.Value, nil, nil)
	if err != nil || string(again) != string(src) {
		t.Errorf("written and undone, the source isn't as it was: %v\n%s", err, again)
	}

	items := "package golit\n\nvar items = []Item{{Name: \"a\", Color: ColorBlue, Price: 2}}\n"
	il, err := Read([]byte(items), "items", []Item(nil), spellings(t))
	if err != nil {
		t.Fatal(err)
	}
	gone := slices.Clone(il.Value)
	gone[0].Color = 0
	out, back2, err := il.Write(gone, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	again, _, err = back2.Write(il.Value, nil, nil)
	if err != nil || string(again) != items {
		t.Errorf("a field taken out and put back is elsewhere: %v\n%s\n%s", err, out, again)
	}
}

// Several literals of a file are written at once, each read back; literals of different
// parses aren't.
func TestWriteAll(t *testing.T) {
	src := stripFile(t)
	f, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	strip, err := ReadVar(f, "strip", Strip{}, stripSpellings())
	if err != nil {
		t.Fatal(err)
	}
	other, err := ReadVar(f, "other", []int(nil), stripSpellings())
	if err != nil {
		t.Fatal(err)
	}
	now := cloneStrip(strip.Value)
	now.Size = image.Pt(8, 8)
	out, err := WriteAll(strip.Change(now, nil, nil), other.Change([]int{1, 2, 3}, nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "Size: image.Pt(8, 8),") || !strings.Contains(string(out), "var other = []int{1, 2, 3}") {
		t.Errorf("wrote\n%s", out)
	}
	g, _ := Parse(src)
	again, _ := ReadVar(g, "other", []int(nil), stripSpellings())
	if _, err := WriteAll(strip.Change(now, nil, nil), again.Change(nil, nil, nil)); err == nil {
		t.Error("wrote literals of two parses at once")
	}
}

// A literal no var holds alone is written back where its finder finds it.
func TestReadAtWritesBack(t *testing.T) {
	src := []byte("package golit\n\nvar held = keep([]Key{{Role: \"a\", Step: 1}})\n")
	f, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	find := func(f *File) (ast.Expr, bool) {
		e, ok := f.Var("held")
		if call, isCall := e.(*ast.CallExpr); ok && isCall && len(call.Args) == 1 {
			return call.Args[0], true
		}
		return nil, false
	}
	l, err := ReadAt(f, find, []Key(nil), Spellings{})
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := l.Write([]Key{{Role: "a", Step: 2}}, nil, nil)
	if err != nil || !strings.Contains(string(out), `keep([]Key{{Role: "a", Step: 2}})`) {
		t.Errorf("wrote %s: %v", out, err)
	}
	e, _ := find(f)
	if _, _, err := ReadExpr(f, e, []Key(nil), Spellings{}).Write(nil, nil, nil); err == nil {
		t.Error("wrote an expression with no way to find it again")
	}
}

// A copy of an element is its source with the copy's edits, its comments and spellings
// kept.
func TestCopy(t *testing.T) {
	l := readShelfFile(t)
	item := *l.Value.ByName["vase"]
	item.Name = "urn"
	text, _, err := l.Copy(`ByName["vase"]`, &item, nil)
	if err != nil || text != `{Name: "urn", Flags: FlagB}` {
		t.Errorf("copied %s: %v", text, err)
	}
	rug := l.Value.Items[1]
	rug.Price = 1
	text, _, err = l.Copy("Items[1]", rug, nil)
	if err != nil || text != `{Name: "rug", Color: ColorGreen, Price: 1, Tags: []string{"soft", "red"}, Rule: double(3)}` {
		t.Errorf("copied %s: %v", text, err)
	}
	if _, _, err := l.Copy("Items[9]", rug, nil); err == nil {
		t.Error("copied an element that isn't there")
	}
	if _, _, err := l.Copy("Items[1]", "rug", nil); err == nil {
		t.Error("copied a value of another type")
	}
}

// A var declared goes in the last var block, under its comment, and taken out again the
// source is as it was; with no block, it goes at the end.
func TestAppendVarAndRemoveVar(t *testing.T) {
	src := []byte("package golit\n\nvar (\n\ta = Strip{}\n)\n\nvar b = 1\n")
	sp := stripSpellings()
	tones := []Tone{tone(0x0a0b0c)}
	out, err := AppendVar(src, "c", "c is new.", tones, sp, map[string]Spelling{"[0]": {Kind: Call, Name: "tone", Args: []any{uint32(0x0a0b0c)}}})
	if err != nil {
		t.Fatal(err)
	}
	if want := "\ta = Strip{}\n\t// c is new.\n\tc = []Tone{tone(0x0a0b0c)}\n)"; !strings.Contains(string(out), want) {
		t.Errorf("declared\n%s", out)
	}
	if _, err := AppendVar(out, "c", "", tones, sp, nil); err == nil {
		t.Error("declared c twice")
	}
	if _, err := AppendVar(out, "d", "", tones, sp, map[string]Spelling{"[0]": {Kind: Call, Name: "tone", Args: []any{uint32(1)}}}); err == nil {
		t.Error("declared a var that doesn't read back as its value")
	}
	back, err := RemoveVar(out, "c")
	if err != nil || string(back) != string(src) {
		t.Errorf("removed to %s: %v", back, err)
	}
	alone := []byte("package golit\n\nvar b = 1\n")
	out, err = AppendVar(alone, "c", "", Raw("other"), sp, nil)
	if err != nil || !strings.HasSuffix(string(out), "var b = 1\n\nvar c = other\n") {
		t.Errorf("declared %s: %v", out, err)
	}
	if back, err := RemoveVar(out, "c"); err != nil || string(back) != string(alone) {
		t.Errorf("removed to %s: %v", back, err)
	}
	if _, err := RemoveVar(alone, "c"); err == nil {
		t.Error("removed a var that isn't there")
	}
}

// An entry is added to the arguments of the call a var is, in their style.
func TestAppendEntryToACall(t *testing.T) {
	for _, tc := range []struct{ src, want string }{
		{"package golit\n\nvar all = join(\n\ta,\n\tb,\n)\n", "\tb,\n\tc,\n)"},
		{"package golit\n\nvar all = join(a, b)\n", "join(a, b, c)"},
		{"package golit\n\nvar all = join()\n", "join(c)"},
	} {
		out, err := AppendEntry([]byte(tc.src), "all", nil, Raw("c"), Spellings{})
		if err != nil || !strings.Contains(string(out), tc.want) {
			t.Errorf("appended to %q: %s, %v", tc.src, out, err)
		}
	}
}

func TestValueAt(t *testing.T) {
	l := readShelfFile(t)
	v := reflect.ValueOf(l.Value)
	for path, want := range map[string]any{
		"Items[1].Name":       "rug",
		`ByName["cup"].At`:    image.Pt(3, 4),
		`Glyphs['o'].Role`:    "ink",
		"Items[0].Tags":       []string(nil),
		"Items[3].Extra.Text": "clay",
	} {
		got, ok := valueAt(v, path)
		if !ok || !reflect.DeepEqual(got.Interface(), want) {
			t.Errorf("%s is %v, %v; want %v", path, got, ok, want)
		}
	}
	for _, path := range []string{"Items[9]", "Nope", `ByName["mug"]`, "Items[x]"} {
		if _, ok := valueAt(v, path); ok {
			t.Errorf("found a value at %s", path)
		}
	}
}
