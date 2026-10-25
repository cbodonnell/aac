package golit

import (
	"go/ast"
	"image"
	"reflect"
	"slices"
	"testing"
)

type Color uint8

const (
	ColorRed Color = iota
	ColorBlue
	ColorGreen
)

type Flag uint8

const (
	FlagA Flag = 1 << iota
	FlagB
	FlagC
)

type Key struct {
	Role string
	Step int
}

type Item struct {
	Name  string
	At    image.Point
	Color Color
	Flags Flag
	Price float64
	Tags  []string
	Rule  func(int) int
	Extra any
}

type Note struct{ Text string }

type Shelf struct {
	Items  []Item
	ByName map[string]*Item
	Glyphs map[rune]Key
	Shades []string
	Count  int
	label  string
}

// colorConsts are Color's constants, as a package's source declares them.
const colorSrc = `package golit

type Color uint8

const (
	ColorRed Color = iota
	ColorBlue
	ColorGreen
)

const colorLast = ColorGreen
`

const flagSrc = `package golit

type Flag uint8

const (
	FlagA Flag = 1 << iota
	FlagB
	FlagC
)
`

const shelfSrc = `package golit

import "image"

// shelf is planted source.
var shelf = Shelf{
	Items: []Item{
		{Name: "lamp", At: image.Pt(1, -2), Color: ColorBlue, Flags: FlagA | FlagC, Price: 2.5},
		{Name: "rug", Color: ColorGreen, Tags: []string{"soft", "red"}, Rule: double(3)},
		{Name: "jar", Rule: func(n int) int { return n }, Extra: Note{"fragile"}},
		{Name: "pot", Extra: Note{Text: "clay"}, Price: price("pot")},
	},
	ByName: map[string]*Item{
		"vase": {Name: "vase", Flags: FlagB},
		"cup":  &Item{Name: "cup", At: image.Point{3, 4}},
	},
	Glyphs: map[rune]Key{
		'o': {"ink", 0},
		'1': {Role: "skin", Step: 2},
	},
	Shades: warm,
	Count: compute(),
	label: "top",
}
`

func double(n int) func(int) int {
	return func(x int) int { return n * x }
}

func price(name string) float64 {
	if name == "" {
		panic("no name")
	}
	return float64(len(name))
}

func spellings(t *testing.T) Spellings {
	t.Helper()
	colors, err := ReadConsts(reflect.TypeFor[Color](), []byte(colorSrc))
	if err != nil {
		t.Fatal(err)
	}
	flags, err := ReadConsts(reflect.TypeFor[Flag](), []byte(flagSrc))
	if err != nil {
		t.Fatal(err)
	}
	return Spellings{
		Consts: map[reflect.Type]Consts{colors.Type: colors, flags.Type: flags},
		Vars:   map[string]any{"warm": []string{"red", "orange"}},
		Helpers: map[string]Helper{
			"double": {Func: double, Label: "double", Args: []string{"n"}},
			"price":  {Func: price, Label: "price", Args: []string{"name"}},
		},
		Types: map[string]reflect.Type{"Note": reflect.TypeFor[Note](), "Key": reflect.TypeFor[Key]()},
	}
}

func readShelf(t *testing.T, built Shelf) *Lit[Shelf] {
	t.Helper()
	l, err := Read([]byte(shelfSrc), "shelf", built, spellings(t))
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestReadDecodesTheLiteral(t *testing.T) {
	jar := func(n int) int { return n + 1 }
	l := readShelf(t, Shelf{Items: []Item{{}, {}, {Rule: jar, Extra: Note{"built"}}}, Count: 7})
	v := l.Value
	if len(v.Items) != 4 {
		t.Fatalf("%d items, want 4", len(v.Items))
	}
	lamp := v.Items[0]
	if lamp.Name != "lamp" || lamp.At != image.Pt(1, -2) || lamp.Color != ColorBlue || lamp.Flags != FlagA|FlagC || lamp.Price != 2.5 {
		t.Errorf("lamp is %+v", lamp)
	}
	rug := v.Items[1]
	if rug.Color != ColorGreen || !slices.Equal(rug.Tags, []string{"soft", "red"}) || rug.Rule == nil || rug.Rule(2) != 6 {
		t.Errorf("rug is %+v", rug)
	}
	if v.Items[2].Rule == nil || v.Items[2].Rule(1) != 2 {
		t.Error("the jar's opaque rule isn't the build's")
	}
	if v.Items[2].Extra != (Note{"fragile"}) || v.Items[3].Extra != (Note{"clay"}) {
		t.Errorf("extras are %v and %v, read as the build's type", v.Items[2].Extra, v.Items[3].Extra)
	}
	if v.Items[3].Price != 3 {
		t.Errorf("the pot's price is %v, made by its helper", v.Items[3].Price)
	}
	if vase := v.ByName["vase"]; vase == nil || vase.Name != "vase" || vase.Flags != FlagB {
		t.Errorf("vase is %+v", vase)
	}
	if cup := v.ByName["cup"]; cup == nil || cup.At != image.Pt(3, 4) {
		t.Errorf("cup is %+v", cup)
	}
	if v.Glyphs['o'] != (Key{"ink", 0}) || v.Glyphs['1'] != (Key{"skin", 2}) {
		t.Errorf("glyphs are %v", v.Glyphs)
	}
	if !slices.Equal(v.Shades, []string{"red", "orange"}) || v.Count != 7 || v.label != "top" {
		t.Errorf("shades %v, count %d, label %q", v.Shades, v.Count, v.label)
	}
}

func TestReadSpellings(t *testing.T) {
	l := readShelf(t, Shelf{Count: 7})
	for path, want := range map[string]Spelling{
		"Items[0].Color":         {Kind: Const, Name: "ColorBlue", Source: "ColorBlue"},
		"Items[0].Flags":         {Kind: Const, Name: "FlagA | FlagC", Source: "FlagA | FlagC"},
		"Shades":                 {Kind: Var, Name: "warm", Source: "warm"},
		"Count":                  {Kind: Opaque, Source: "compute()"},
		"Items[2].Rule":          {Kind: Opaque, Source: "func(n int) int { return n }"},
		"Items[3].Price":         {Kind: Call, Name: "price", Args: []any{"pot"}, Source: `price("pot")`},
		"Items[1].Rule":          {Kind: Call, Name: "double", Args: []any{3}, Source: "double(3)"},
		"ByName[\"vase\"].Flags": {Kind: Const, Name: "FlagB", Source: "FlagB"},
	} {
		if got := l.Spelled[path]; !reflect.DeepEqual(got, want) {
			t.Errorf("%s is spelled %+v, want %+v", path, got, want)
		}
	}
	for _, plain := range []string{"Items[0].At", "Items[0].Name", "Glyphs['o']", "Glyphs['o'].Role", "Items[1].Rule(0)"} {
		if s, ok := l.Spelled[plain]; ok {
			t.Errorf("%s is spelled %+v, want plain", plain, s)
		}
	}
	if got := l.Opaque(); !slices.Equal(got, []string{"Count", "Items[2].Rule"}) {
		t.Errorf("opaque at %v", got)
	}
	paths := l.Paths()
	for _, want := range []string{"", "Items", "Items[3]", "Items[3].Extra.Text", "ByName[\"cup\"].At", "Glyphs['1'].Step", "label"} {
		if !slices.Contains(paths, want) {
			t.Errorf("no path %s among %v", want, paths)
		}
	}
}

// An opaque value the build has nothing for is zero.
func TestReadOpaqueWithoutTheBuild(t *testing.T) {
	l := readShelf(t, Shelf{})
	if l.Value.Count != 0 || l.Value.Items[2].Rule != nil {
		t.Errorf("count %d, rule set %v, want zero", l.Value.Count, l.Value.Items[2].Rule != nil)
	}
	if l.Spelled["Count"].Kind != Opaque {
		t.Error("count isn't opaque")
	}
}

// A helper that fails, by panicking, or a call with an argument that isn't read, is opaque.
func TestReadCallsThatFail(t *testing.T) {
	src := `package golit

var items = []Item{
	{Price: price("")},
	{Price: price(name)},
	{Rule: double(1, 2)},
}
`
	sp := spellings(t)
	l, err := Read([]byte(src), "items", []Item{{Price: 9}, {}, {}}, sp)
	if err != nil {
		t.Fatal(err)
	}
	if got := l.Opaque(); !slices.Equal(got, []string{"[0].Price", "[1].Price", "[2].Rule"}) {
		t.Errorf("opaque at %v", got)
	}
	if l.Value[0].Price != 9 {
		t.Errorf("price %v, want the build's", l.Value[0].Price)
	}
	if _, ok := l.Spelled["[1].Price(0)"]; ok {
		t.Error("a failed call's argument is still spelled")
	}
}

// A registered type names what a literal in an interface is.
func TestReadInterfaceOfARegisteredType(t *testing.T) {
	src := `package golit

var extras = []any{Note{"a"}, &Item{Name: "b"}, nil, 3}
`
	sp := Spellings{Types: map[string]reflect.Type{"Note": reflect.TypeFor[Note](), "Item": reflect.TypeFor[Item]()}}
	l, err := Read[[]any]([]byte(src), "extras", nil, sp)
	if err != nil {
		t.Fatal(err)
	}
	if l.Value[0] != (Note{"a"}) || l.Value[2] != nil {
		t.Errorf("extras are %v", l.Value)
	}
	if got := l.Opaque(); !slices.Equal(got, []string{"[1]", "[3]"}) {
		t.Errorf("opaque at %v: an & of a value type, and an untyped constant", got)
	}
}

// Outside their package, constants are qualified.
func TestReadQualifiedConsts(t *testing.T) {
	src := `package other

import "golit"

var colors = map[golit.Color]golit.Color{
	golit.ColorRed: golit.ColorBlue,
	ColorBlue:     golit.ColorGreen,
}
`
	l, err := Read[map[Color]Color]([]byte(src), "colors", nil, spellings(t))
	if err != nil {
		t.Fatal(err)
	}
	if got := l.Value; len(got) != 0 {
		// The second key isn't a constant of the package, so the map isn't read.
		t.Errorf("colors are %v, want the build's, nil", got)
	}
	src = `package other

var colors = map[golit.Color]golit.Color{golit.ColorRed: golit.ColorBlue}
`
	l, err = Read[map[Color]Color]([]byte(src), "colors", nil, spellings(t))
	if err != nil {
		t.Fatal(err)
	}
	if l.Value[ColorRed] != ColorBlue || l.Spelled["[0]"].Name != "ColorBlue" {
		t.Errorf("colors are %v, spelled %v", l.Value, l.Spelled)
	}
}

// A literal passed to a function reads into an interface as the build's type.
func TestReadExprOfAnInterface(t *testing.T) {
	src := `package golit

var all = build([]Key{{"a", 1}, {Role: "b"}})
`
	f, err := Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	e, _ := f.Var("all")
	l := ReadExpr[any](f, e.(*ast.CallExpr).Args[0], []Key{}, Spellings{})
	if got, ok := l.Value.([]Key); !ok || !slices.Equal(got, []Key{{"a", 1}, {"b", 0}}) {
		t.Errorf("read %#v", l.Value)
	}
	if len(l.Opaque()) != 0 {
		t.Errorf("opaque at %v", l.Opaque())
	}
}

func TestReadConsts(t *testing.T) {
	other := `package golit

const ColorLast Color = colorLast + 0

const (
	ColorAlias = ColorBlue
	_ Color = iota
	ColorFour
)
`
	c, err := ReadConsts(reflect.TypeFor[Color](), []byte(colorSrc), []byte(other))
	if err != nil {
		t.Fatal(err)
	}
	if c.Pkg != "golit" {
		t.Errorf("package %q", c.Pkg)
	}
	if got := c.Names(); !slices.Equal(got, []string{"ColorRed", "ColorBlue", "ColorGreen", "ColorLast", "ColorFour"}) {
		t.Errorf("names %v: an untyped alias isn't one", got)
	}
	for name, want := range map[string]Color{"ColorRed": 0, "ColorGreen": 2, "ColorLast": 2, "ColorFour": 2} {
		if v, ok := c.Value(name); !ok || v != want {
			t.Errorf("%s is %v, want %v", name, v, want)
		}
	}
	if name, ok := c.Name(ColorGreen); !ok || name != "ColorGreen" {
		t.Errorf("green is named %q", name)
	}
	if _, ok := c.Name(Color(9)); ok {
		t.Error("9 is named")
	}
	f, err := ReadConsts(reflect.TypeFor[Flag](), []byte(flagSrc))
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := f.Value("FlagC"); v != Flag(4) {
		t.Errorf("FlagC is %v", v)
	}
	if _, err := ReadConsts(reflect.TypeFor[Key](), []byte(flagSrc)); err == nil {
		t.Error("read constants of a type with none")
	}
}

func TestCall(t *testing.T) {
	sp := spellings(t)
	v, err := sp.Call("double", []any{4})
	if err != nil {
		t.Fatal(err)
	}
	if v.(func(int) int)(2) != 8 {
		t.Error("double(4) doesn't double 4 times")
	}
	if _, err := sp.Call("price", []any{""}); err == nil {
		t.Error("a helper that panics made a value")
	}
	if _, err := sp.Call("double", []any{"x"}); err == nil {
		t.Error("a helper made a value from the wrong type")
	}
	if _, err := sp.Call("triple", nil); err == nil {
		t.Error("an unknown helper made a value")
	}
}

func TestWrittenAndSource(t *testing.T) {
	l := readShelf(t, Shelf{})
	if !l.Written("Items[0].At") || l.Written("Items[1].At") {
		t.Error("written isn't what the source spells")
	}
	if got := l.Source("Items[0].At"); got != "image.Pt(1, -2)" {
		t.Errorf("source %q", got)
	}
	if got := l.Source("Items[1].At"); got != "" {
		t.Errorf("source of a path not written %q", got)
	}
}

func TestUnder(t *testing.T) {
	for _, tc := range []struct {
		p, path string
		want    bool
	}{
		{"Items[0].At", "Items", true},
		{"Items[0].At", "Items[0]", true},
		{"Items[0]", "Items[0]", true},
		{"Items[1].Rule(0)", "Items[1].Rule", true},
		{"Items", "", true},
		{"ItemsByName", "Items", false},
		{"Items", "Items[0]", false},
	} {
		if got := Under(tc.p, tc.path); got != tc.want {
			t.Errorf("Under(%q, %q) = %v", tc.p, tc.path, got)
		}
	}
}
