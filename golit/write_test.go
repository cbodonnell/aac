package golit

import (
	"go/format"
	"image"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// shelfFile is the planted source, formatted, as a saved file is.
func shelfFile(t *testing.T) []byte {
	t.Helper()
	src, err := format.Source([]byte(shelfSrc))
	if err != nil {
		t.Fatal(err)
	}
	return src
}

func readShelfFile(t *testing.T) *Lit[Shelf] {
	t.Helper()
	l, err := Read(shelfFile(t), "shelf", Shelf{Count: 7}, spellings(t))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// cloneShelf is a copy of a shelf whose lists and maps change apart from it.
func cloneShelf(s Shelf) Shelf {
	c := s
	c.Items = slices.Clone(s.Items)
	for i := range c.Items {
		c.Items[i].Tags = slices.Clone(c.Items[i].Tags)
	}
	c.ByName = map[string]*Item{}
	for k, v := range s.ByName {
		item := *v
		c.ByName[k] = &item
	}
	c.Glyphs = maps.Clone(s.Glyphs)
	c.Shades = slices.Clone(s.Shades)
	return c
}

// spelledOf are a literal's spellings that aren't plain, for writing its value again.
func spelledOf[T any](l *Lit[T]) map[string]Spelling {
	out := map[string]Spelling{}
	for p, s := range l.Spelled {
		if s.Kind != Const {
			out[p] = s
		}
	}
	return out
}

func TestWriteUneditedChangesNothing(t *testing.T) {
	l := readShelfFile(t)
	splices, err := l.Splices(l.Value, nil, nil)
	if err != nil || len(splices) != 0 {
		t.Fatalf("splices %v: %v", splices, err)
	}
	out, _, err := l.Write(cloneShelf(l.Value), nil, nil)
	if err != nil || string(out) != string(shelfFile(t)) {
		t.Errorf("written unedited, the source changed: %v\n%s", err, out)
	}
}

// edits are changes to the shelf, with the spellings and origins they write with, and
// what the source must then say.
var edits = []struct {
	name    string
	edit    func(s *Shelf, spelled map[string]Spelling, origins map[string][]int)
	want    []string
	notWant []string
}{
	{"field changed", func(s *Shelf, _ map[string]Spelling, _ map[string][]int) {
		s.Items[0].Price = 3.75
		s.Items[0].At = image.Pt(5, 6)
	}, []string{"Price: 3.75", "At: image.Pt(5, 6)"}, nil},
	{"constant respelled", func(s *Shelf, _ map[string]Spelling, _ map[string][]int) {
		s.Items[0].Color = ColorGreen
		s.Items[0].Flags = FlagB | FlagC
	}, []string{"Color: ColorGreen", "Flags: FlagB | FlagC"}, nil},
	{"field emptied", func(s *Shelf, _ map[string]Spelling, _ map[string][]int) {
		s.Items[0].Price = 0
	}, nil, []string{"2.5"}},
	{"field added", func(s *Shelf, _ map[string]Spelling, _ map[string][]int) {
		s.Items[2].Price = 1
		s.Items[0].Tags = []string{"new"}
	}, []string{`{Name: "jar", Price: 1, Rule: func(n int) int { return n }, Extra: Note{"fragile"}}`, "Price: 2.5, Tags: []string{\n\t\t\t\"new\",\n\t\t}},"}, nil},
	{"element added", func(s *Shelf, _ map[string]Spelling, _ map[string][]int) {
		s.Items = append(s.Items, Item{Name: "bowl", Color: ColorBlue, Extra: Note{"deep"}})
	}, []string{"\t\t{Name: \"bowl\", Color: ColorBlue, Extra: Note{Text: \"deep\"}},\n\t},"}, nil},
	{"element inserted", func(s *Shelf, _ map[string]Spelling, origins map[string][]int) {
		s.Items = slices.Insert(s.Items, 1, Item{Name: "mat"})
		origins["Items"] = []int{0, -1, 1, 2, 3}
	}, []string{"Price: 2.5},\n\t\t{Name: \"mat\"},\n\t\t{Name: \"rug\""}, nil},
	{"element removed", func(s *Shelf, _ map[string]Spelling, origins map[string][]int) {
		s.Items = slices.Delete(s.Items, 0, 1)
		origins["Items"] = []int{1, 2, 3}
	}, nil, []string{"lamp"}},
	{"elements reordered", func(s *Shelf, spelled map[string]Spelling, origins map[string][]int) {
		s.Items[0], s.Items[3] = s.Items[3], s.Items[0]
		s.Items[3].Name = "lantern"
		origins["Items"] = []int{3, 1, 2, 0}
	}, []string{"Items: []Item{\n\t\t{Name: \"pot\"", "{Name: \"lantern\", At: image.Pt(1, -2)"}, nil},
	{"inline list element added and removed", func(s *Shelf, _ map[string]Spelling, _ map[string][]int) {
		s.Items[1].Tags = []string{"soft", "blue", "warm"}
	}, []string{`"blue"`, `"warm"`}, nil},
	{"map key added", func(s *Shelf, _ map[string]Spelling, _ map[string][]int) {
		s.ByName["bowl"] = &Item{Name: "bowl", At: image.Pt(1, 1)}
		s.Glyphs['x'] = Key{"shade", 3}
	}, []string{"\"cup\":  &Item{Name: \"cup\", At: image.Point{3, 4}},\n\t\t\"bowl\": {Name: \"bowl\", At: image.Pt(1, 1)},", `'x': {Role: "shade", Step: 3},`}, nil},
	{"pointer element changed", func(s *Shelf, _ map[string]Spelling, _ map[string][]int) {
		s.ByName["vase"].Flags = FlagA
		s.ByName["cup"].Name = "mug"
	}, []string{`"vase": {Name: "vase", Flags: FlagA}`, `&Item{Name: "mug"`}, nil},
	{"interface field changed", func(s *Shelf, _ map[string]Spelling, _ map[string][]int) {
		s.Items[2].Extra = Note{"broken"}
		s.Items[1].Extra = Note{"new"}
		s.Items[3].Extra = Key{"clay", 1}
	}, []string{`Extra: Note{"broken"}`, `Rule: double(3), Extra: Note{Text: "new"}}`, `Extra: Key{Role: "clay", Step: 1}`}, nil},
	{"var respelled", func(s *Shelf, spelled map[string]Spelling, _ map[string][]int) {
		s.Shades = []string{"blue"}
	}, []string{"Shades: []string{\n\t\t\"blue\",\n\t},"}, nil},
	{"helper's argument changed", func(s *Shelf, spelled map[string]Spelling, _ map[string][]int) {
		s.Items[1].Rule = double(5)
		spelled["Items[1].Rule"] = Spelling{Kind: Call, Name: "double", Args: []any{5}}
		s.Items[3].Price = 4
		spelled["Items[3].Price"] = Spelling{Kind: Call, Name: "price", Args: []any{"bowl"}}
	}, []string{"Rule: double(5)", `Price: price("bowl")`}, nil},
	{"helper to plain", func(s *Shelf, spelled map[string]Spelling, _ map[string][]int) {
		s.Items[1].Rule = nil
		spelled["Items[1].Rule"] = Spelling{Kind: Plain}
	}, nil, []string{"double(3)"}},
	{"char key edited", func(s *Shelf, _ map[string]Spelling, _ map[string][]int) {
		s.Glyphs['o'] = Key{"ink", 1}
	}, []string{`'o': {"ink", 1},`}, nil},
}

// Each edit is written, reads back as edited, and written undone is the source again.
func TestWriteEdits(t *testing.T) {
	for _, tc := range edits {
		t.Run(tc.name, func(t *testing.T) {
			l := readShelfFile(t)
			src := shelfFile(t)
			now := cloneShelf(l.Value)
			spelled, origins := map[string]Spelling{}, map[string][]int{}
			tc.edit(&now, spelled, origins)
			out, back, err := l.Write(now, spelled, origins)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.want {
				if !strings.Contains(string(out), want) {
					t.Errorf("written source has no %q:\n%s", want, out)
				}
			}
			for _, not := range tc.notWant {
				if strings.Contains(string(out), not) {
					t.Errorf("written source still has %q:\n%s", not, out)
				}
			}
			if strings.Contains(string(out), "compute()") == false || !strings.Contains(string(out), "func(n int) int { return n }") {
				t.Error("an opaque value was lost")
			}
			// Back: the source's own value and spellings, and the origins turned round.
			undo := map[string][]int{}
			for p, ord := range origins {
				back := make([]int, len(l.Value.Items))
				for i := range back {
					back[i] = -1
				}
				for k, o := range ord {
					if o >= 0 {
						back[o] = k
					}
				}
				undo[p] = back
			}
			again, _, err := back.Write(l.Value, spelledOf(l), undo)
			if err != nil {
				t.Fatalf("undoing: %v", err)
			}
			if string(again) != string(src) {
				t.Errorf("written and undone, the source isn't as it was:\n%s", again)
			}
		})
	}
}

// A comment on an element's line goes with it, and one on a line of its own stays.
func TestWriteRemovesComments(t *testing.T) {
	src := `package golit

var items = []Item{
	// The first.
	{Name: "a"}, // gone
	{Name: "b"},
}
`
	l, err := Read[[]Item]([]byte(src), "items", nil, spellings(t))
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := l.Write([]Item{{Name: "b"}}, nil, map[string][]int{"": {1}})
	if err != nil {
		t.Fatal(err)
	}
	want := "var items = []Item{\n\t// The first.\n\t{Name: \"b\"},\n}\n"
	if !strings.HasSuffix(string(out), want) {
		t.Errorf("wrote\n%s", out)
	}
}

// A list emptied goes from its struct, and one added is a new key.
func TestWriteListsEmptiedAndAdded(t *testing.T) {
	l := readShelfFile(t)
	now := cloneShelf(l.Value)
	now.ByName = nil
	now.Items = now.Items[:0]
	out, back, err := l.Write(now, nil, map[string][]int{"Items": {}})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "Items:") || strings.Contains(string(out), "ByName") {
		t.Errorf("emptied lists are still there:\n%s", out)
	}
	now.Items = []Item{{Name: "new"}}
	out, _, err = back.Write(now, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "Items: []Item{\n\t\t{Name: \"new\"},\n\t},") {
		t.Errorf("no new list:\n%s", out)
	}
}

// A write whose value can't be written, or reads back otherwise, is refused.
func TestWriteRefuses(t *testing.T) {
	l := readShelfFile(t)
	now := cloneShelf(l.Value)
	now.Items[0].Rule = func(int) int { return 0 }
	if _, _, err := l.Write(now, nil, nil); err == nil {
		t.Error("wrote a function plainly")
	}
	now = cloneShelf(l.Value)
	if _, _, err := l.Write(now, map[string]Spelling{"Count": {Kind: Opaque, Source: "compute("}}, nil); err == nil {
		t.Error("wrote source that doesn't parse")
	}
	now = cloneShelf(l.Value)
	if _, _, err := l.Write(now, map[string]Spelling{"Items[0].Price": {Kind: Call, Name: "price", Args: []any{"bowl"}}}, nil); err == nil {
		t.Error("wrote a call whose value isn't the value written")
	}
	if _, _, err := l.Write(now, nil, map[string][]int{"Items": {0, 0, 1, 2}}); err == nil {
		t.Error("wrote an element twice from one origin")
	}
}

// A package written that the file doesn't import is imported.
func TestWriteImports(t *testing.T) {
	src := `package golit

var items = []Item{{Name: "a"}}
`
	l, err := Read[[]Item]([]byte(src), "items", nil, spellings(t))
	if err != nil {
		t.Fatal(err)
	}
	out, _, err := l.Write([]Item{{Name: "a", At: image.Pt(1, 2)}}, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "import \"image\"\n") || !strings.Contains(string(out), "{Name: \"a\", At: image.Pt(1, 2)}") {
		t.Errorf("wrote\n%s", out)
	}
}

func TestFormat(t *testing.T) {
	sp := spellings(t)
	text, imports, err := Format(Shelf{
		Items:  []Item{{Name: "a", Color: ColorBlue, Flags: FlagA | FlagB}, {At: image.Pt(1, 2), Rule: double(2), Extra: &Item{Name: "x"}}},
		ByName: map[string]*Item{"b": {Name: "b"}, "a": {Price: 1.5}},
		Glyphs: map[rune]Key{'a': {"ink", 0}},
		Shades: []string{"red"},
		Count:  0,
		label:  "here",
	}, "golit", sp, map[string]Spelling{"Items[1].Rule": {Kind: Call, Name: "double", Args: []any{2}}})
	if err != nil {
		t.Fatal(err)
	}
	want := `Shelf{
Items: []Item{
{Name: "a", Color: ColorBlue, Flags: FlagA | FlagB},
{At: image.Pt(1, 2), Rule: double(2), Extra: &Item{Name: "x"}},
},
ByName: map[string]*Item{
"a": {Price: 1.5},
"b": {Name: "b"},
},
Glyphs: map[int32]Key{
97: {Role: "ink"},
},
Shades: []string{
"red",
},
label: "here",
}`
	if text != want {
		t.Errorf("formatted\n%s\nwant\n%s", text, want)
	}
	if !reflect.DeepEqual(imports, map[string]string{"image": "image"}) {
		t.Errorf("imports %v", imports)
	}
	if _, _, err := Format(Item{Rule: double(1)}, "golit", sp, nil); err == nil {
		t.Error("formatted a function")
	}
	if _, _, err := Format(Shelf{label: "x"}, "other", sp, nil); err == nil {
		t.Error("formatted an unexported field outside its package")
	}
}

func TestAppendConst(t *testing.T) {
	for _, tc := range []struct {
		name, src, typ, add, want string
	}{
		{"iota", colorSrc, "Color", "ColorGold", "\tColorGreen\n\tColorGold\n)"},
		{"shifted", flagSrc, "Flag", "FlagD", "\tFlagC\n\tFlagD\n)"},
		{"explicit", `package golit

type Map int

const (
	MapA   Map = 1 // the first
	MapBig Map = 12
	Other      = 40
)
`, "Map", "MapNew", "\tMapBig Map = 12\n\tMapNew Map = 13\n\tOther      = 40"},
		{"single", `package golit

type Size int

const Small Size = 2
`, "Size", "Large", "const Small Size = 2\nconst Large Size = 3\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := AppendConst([]byte(tc.src), tc.typ, tc.add)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(out), tc.want) {
				t.Errorf("wrote\n%s", out)
			}
		})
	}
	if _, err := AppendConst([]byte(colorSrc), "Color", "ColorBlue"); err == nil {
		t.Error("added a constant twice")
	}
	if _, err := AppendConst([]byte(colorSrc), "Shade", "ShadeA"); err == nil {
		t.Error("added a constant of a type with none")
	}
	if _, err := AppendConst([]byte("package golit\n\ntype Zone string\n\nconst ZoneA Zone = \"a\"\n"), "Zone", "ZoneB"); err == nil {
		t.Error("numbered a string constant")
	}
}

func TestAppendEntry(t *testing.T) {
	src := `package golit

import "image"

var byColor = map[Color]Item{
	ColorRed: {
		Name: "red",
	},
}

var names = map[string]string{"a": "b"}

var list = []Key{
	{"a", 1},
}
`
	sp := spellings(t)
	out, err := AppendEntry([]byte(src), "byColor", ColorBlue, Item{Name: "blue", At: image.Pt(1, 1)}, sp)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "\tColorBlue: {\n\t\tName: \"blue\",\n\t\tAt:   image.Pt(1, 1),\n\t},\n}") {
		t.Errorf("wrote\n%s", out)
	}
	if _, err := AppendEntry(out, "byColor", ColorBlue, Item{}, sp); err == nil {
		t.Error("added a key twice")
	}
	out, err = AppendEntry(out, "names", "c", Raw("strings.ToUpper(\"d\")"), sp)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `{"a": "b", "c": strings.ToUpper("d")}`) {
		t.Errorf("wrote\n%s", out)
	}
	out, err = AppendEntry(out, "list", nil, Key{"b", 2}, sp)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "\t{\"a\", 1},\n\t{\"b\", 2},\n}") {
		t.Errorf("wrote\n%s", out)
	}
}
