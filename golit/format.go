package golit

import (
	"fmt"
	"go/ast"
	"image/color"
	"path"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"unsafe"

	"github.com/cbodonnell/aac/edit"
)

// Columns is how wide a line written inline may be, a tab counted as TabWidth.
const (
	Columns  = 100
	TabWidth = 4
)

// Raw is an expression written as it is, for a value spelled in a way the codec doesn't
// write itself (&mapX).
type Raw string

var (
	nrgbaType = reflect.TypeFor[color.NRGBA]()
	rawType   = reflect.TypeFor[Raw]()
)

// writer writes values as source in a file of one package, and collects the edits of a
// literal there.
type writer struct {
	file *File
	sp   Spellings
	pkg  string
	// imports are the file's imports, by path, with the names they are used by; used are
	// the packages written that it doesn't import.
	imports map[string]string
	used    map[string]string
	// spelled and origins are what a write was given; was are the source's spellings.
	spelled map[string]Spelling
	origins map[string][]int
	was     map[string]Spelling
	splices []edit.Splice
}

func newWriter(f *File, pkg string, sp Spellings) *writer {
	w := &writer{file: f, sp: sp, pkg: pkg, imports: map[string]string{}, used: map[string]string{}}
	if f == nil {
		return w
	}
	w.pkg = f.pkgName()
	for _, imp := range f.AST.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		name := path.Base(p)
		if imp.Name != nil {
			name = imp.Name.Name
		}
		w.imports[p] = name
	}
	return w
}

// Format is the source of a value as a new literal in a file of package pkg, and the
// packages it names, by path, with the names it uses for them. spelled gives the paths
// written other than plainly, as Splices takes them.
func Format[T any](v T, pkg string, sp Spellings, spelled map[string]Spelling) (string, map[string]string, error) {
	w := newWriter(nil, pkg, sp)
	w.spelled = spelled
	text, err := w.format(reflect.ValueOf(&v).Elem(), "", false, layout{}, 0)
	return text, w.used, err
}

// qualify is the qualifier of a name declared in the package at path, named name.
func (w *writer) qualify(pkgPath, name string) string {
	if pkgPath == "" {
		return ""
	}
	if n, ok := w.imports[pkgPath]; ok {
		return n + "."
	}
	if name == w.pkg {
		return ""
	}
	w.used[pkgPath] = name
	return name + "."
}

// typeName is how the file names a type.
func (w *writer) typeName(t reflect.Type) string {
	if t.Name() != "" {
		if t.PkgPath() == "" {
			return t.Name()
		}
		pkg, _, _ := strings.Cut(t.String(), ".")
		return w.qualify(t.PkgPath(), pkg) + t.Name()
	}
	switch t.Kind() {
	case reflect.Slice:
		return "[]" + w.typeName(t.Elem())
	case reflect.Array:
		return fmt.Sprintf("[%d]%s", t.Len(), w.typeName(t.Elem()))
	case reflect.Map:
		return "map[" + w.typeName(t.Key()) + "]" + w.typeName(t.Elem())
	case reflect.Pointer:
		return "*" + w.typeName(t.Elem())
	}
	return t.String()
}

// literalType is the name a composite literal of t held by an interface is spelled with:
// its registered name, or its own.
func (w *writer) literalType(t reflect.Type) string {
	for _, name := range slices.Sorted(mapKeys(w.sp.Types)) {
		if w.sp.Types[name] == t {
			return name
		}
	}
	return w.typeName(t)
}

func mapKeys[V any](m map[string]V) func(func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}

// constant is a value's constant name, qualified, or constants of it joined by | for
// flags that no one constant is.
func (w *writer) constant(v reflect.Value) (string, bool) {
	c, ok := w.sp.Consts[v.Type()]
	if !ok {
		return "", false
	}
	q := w.qualify(c.Type.PkgPath(), c.Pkg)
	if name, ok := c.Name(interfaceOf(v)); ok {
		return q + name, true
	}
	var bits uint64
	switch {
	case v.CanInt() && v.Int() > 0:
		bits = uint64(v.Int())
	case v.CanUint():
		bits = v.Uint()
	default:
		return "", false
	}
	var names []string
	var got uint64
	for _, name := range c.names {
		cv := c.values[name]
		var b uint64
		if cv.CanInt() {
			b = uint64(cv.Int())
		} else {
			b = cv.Uint()
		}
		if b != 0 && b&(b-1) == 0 && bits&b != 0 && got&b == 0 {
			names = append(names, q+name)
			got |= b
		}
	}
	if got != bits || len(names) == 0 {
		return "", false
	}
	return strings.Join(names, " | "), true
}

// interfaceOf is a value as an any, read out of an unexported field if it is one.
func interfaceOf(v reflect.Value) any {
	if v.CanInterface() {
		return v.Interface()
	}
	if v.CanAddr() {
		return reflect.NewAt(v.Type(), unsafe.Pointer(v.UnsafeAddr())).Elem().Interface()
	}
	c := reflect.New(v.Type()).Elem()
	copyInto(c, v)
	return c.Interface()
}

// copyInto copies a value that may have been read out of an unexported field, which Set
// refuses, kind by kind.
func copyInto(dst, src reflect.Value) {
	switch src.Kind() {
	case reflect.Bool:
		dst.SetBool(src.Bool())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		dst.SetInt(src.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		dst.SetUint(src.Uint())
	case reflect.Float32, reflect.Float64:
		dst.SetFloat(src.Float())
	case reflect.String:
		dst.SetString(src.String())
	default:
		dst.Set(src)
	}
}

// fieldOf is a struct's field, readable whether exported or not.
func fieldOf(s reflect.Value, i int) reflect.Value {
	sf := s.Type().Field(i)
	if sf.IsExported() {
		return s.Field(i)
	}
	return readable(s, sf)
}

// layout is how a composite value is laid out: one element a line, inline when it fits,
// or neither, as the codec writes its type alone; and a struct by position, every field
// written in order.
type layout struct {
	lines, inline, positional bool
}

// format writes a value at a path: as spelled there, or plainly. elided leaves out a
// composite literal's type, as an element of a list or map may. lay lays out a composite
// value itself, not what is in it. indent is the columns the value starts in.
func (w *writer) format(v reflect.Value, path string, elided bool, lay layout, indent int) (string, error) {
	if s, ok := w.spelled[path]; ok && s.Kind != Plain {
		return w.spell(s, v, path, indent)
	}
	return w.plain(v, path, elided, lay, indent)
}

// spell writes a value as a spelling says.
func (w *writer) spell(s Spelling, v reflect.Value, path string, indent int) (string, error) {
	switch s.Kind {
	case Const:
		if c, ok := w.sp.Consts[v.Type()]; ok {
			if cv, ok := c.values[s.Name]; ok && cv.Equal(v) {
				return w.qualify(c.Type.PkgPath(), c.Pkg) + s.Name, nil
			}
		}
		return w.plain(v, path, false, layout{}, indent)
	case Var:
		if _, ok := w.sp.Vars[s.Name]; !ok {
			return "", fmt.Errorf("%s: no var %s", path, s.Name)
		}
		return s.Name, nil
	case Call:
		hex := w.sp.Helpers[s.Name].Hex
		args := make([]string, len(s.Args))
		for i, a := range s.Args {
			av := reflect.ValueOf(a)
			switch {
			case !av.IsValid():
				args[i] = "nil"
				continue
			case hex > 0 && av.CanUint():
				args[i] = fmt.Sprintf("0x%0*x", hex, av.Uint())
				continue
			case hex > 0 && av.CanInt() && av.Int() >= 0:
				args[i] = fmt.Sprintf("0x%0*x", hex, av.Int())
				continue
			}
			text, err := w.format(av, path+"("+strconv.Itoa(i)+")", false, layout{}, indent)
			if err != nil {
				return "", err
			}
			args[i] = text
		}
		return s.Name + "(" + strings.Join(args, ", ") + ")", nil
	case Opaque:
		if s.Source == "" {
			return "", fmt.Errorf("%s: opaque with no source", path)
		}
		return s.Source, nil
	}
	return w.plain(v, path, false, layout{}, indent)
}

// plain writes a value as the codec writes it alone.
func (w *writer) plain(v reflect.Value, path string, elided bool, lay layout, indent int) (string, error) {
	if v.Type() == rawType {
		return v.String(), nil
	}
	if text, ok := w.constant(v); ok {
		return text, nil
	}
	t := v.Type()
	switch t {
	case pointType:
		if elided {
			return fmt.Sprintf("{%d, %d}", v.Field(0).Int(), v.Field(1).Int()), nil
		}
		return w.qualify("image", "image") + fmt.Sprintf("Pt(%d, %d)", v.Field(0).Int(), v.Field(1).Int()), nil
	case nrgbaType:
		c := interfaceOf(v).(color.NRGBA)
		text := fmt.Sprintf("{0x%02x, 0x%02x, 0x%02x, 0x%02x}", c.R, c.G, c.B, c.A)
		if elided {
			return text, nil
		}
		return w.typeName(t) + text, nil
	}
	switch k := t.Kind(); {
	case k == reflect.Bool:
		return strconv.FormatBool(v.Bool()), nil
	case v.CanInt():
		return strconv.FormatInt(v.Int(), 10), nil
	case v.CanUint():
		return strconv.FormatUint(v.Uint(), 10), nil
	case v.CanFloat():
		return strconv.FormatFloat(v.Float(), 'g', -1, t.Bits()), nil
	case k == reflect.String:
		return strconv.Quote(v.String()), nil
	case k == reflect.Pointer:
		if v.IsNil() {
			return "nil", nil
		}
		if t.Elem().Kind() != reflect.Struct {
			return "", fmt.Errorf("%s: a %s isn't written", path, t)
		}
		body, err := w.composite(v.Elem(), path, lay, indent)
		if err != nil || elided {
			return body, err
		}
		return "&" + w.typeName(t.Elem()) + body, nil
	case k == reflect.Interface:
		if v.IsNil() {
			return "nil", nil
		}
		e := v.Elem()
		switch e.Kind() {
		case reflect.Struct, reflect.Slice, reflect.Array, reflect.Map:
			if e.Type() == pointType {
				return w.plain(e, path, false, lay, indent)
			}
			body, err := w.composite(e, path, lay, indent)
			if err != nil {
				return "", err
			}
			return w.literalType(e.Type()) + body, nil
		case reflect.Pointer:
			if !e.IsNil() && e.Elem().Kind() == reflect.Struct {
				body, err := w.composite(e.Elem(), path, lay, indent)
				if err != nil {
					return "", err
				}
				return "&" + w.literalType(e.Elem().Type()) + body, nil
			}
		}
		text, err := w.plain(e, path, false, lay, indent)
		if err != nil {
			return "", err
		}
		// An untyped constant would take its default type.
		if e.Type().Name() != "" && e.Type().PkgPath() != "" {
			if _, isConst := w.sp.Consts[e.Type()]; !isConst {
				return w.typeName(e.Type()) + "(" + text + ")", nil
			}
		}
		return text, nil
	case k == reflect.Slice && v.IsNil(), k == reflect.Map && v.IsNil(), k == reflect.Func && v.IsNil():
		return "nil", nil
	case k == reflect.Struct, k == reflect.Slice, k == reflect.Array, k == reflect.Map:
		body, err := w.composite(v, path, lay, indent)
		if err != nil || elided {
			return body, err
		}
		return w.typeName(t) + body, nil
	}
	return "", fmt.Errorf("%s: a %s isn't written plainly", path, t)
}

// composite writes a struct, list or map's braces and what is in them: a struct by key,
// its zero fields left out, or by position; a list of composite values or strings one
// element a line, and of anything else, as names and calls, inline when it fits; a map one
// entry a line. lay says otherwise.
func (w *writer) composite(v reflect.Value, path string, lay layout, indent int) (string, error) {
	t := v.Type()
	var items []string
	lines := lay.lines
	switch t.Kind() {
	case reflect.Struct:
		for i := range t.NumField() {
			sf := t.Field(i)
			fv := fieldOf(v, i)
			fpath := join(path, sf.Name)
			if !lay.positional && w.omit(fv, fpath) {
				continue
			}
			if !sf.IsExported() && !w.local(t) {
				return "", fmt.Errorf("%s: unexported outside its package", fpath)
			}
			text, err := w.format(fv, fpath, false, layout{}, indent+TabWidth)
			if err != nil {
				return "", err
			}
			if !lay.positional {
				text = sf.Name + ": " + text
			}
			items = append(items, text)
		}
	case reflect.Slice, reflect.Array:
		for i := range v.Len() {
			text, err := w.format(v.Index(i), index(path, strconv.Itoa(i)), true, layout{}, indent+TabWidth)
			if err != nil {
				return "", err
			}
			items = append(items, text)
		}
		if !lay.inline && perLine(t.Elem()) {
			lines = lines || t.Elem().Kind() == reflect.String || slices.ContainsFunc(items, composed)
		}
	case reflect.Map:
		keys := v.MapKeys()
		sortKeys(keys)
		for _, k := range keys {
			key, err := w.plain(k, "", true, layout{}, indent+TabWidth)
			if err != nil {
				return "", err
			}
			text, err := w.format(v.MapIndex(k), index(path, KeyPath(k, false)), true, layout{}, indent+TabWidth)
			if err != nil {
				return "", err
			}
			items = append(items, key+": "+text)
		}
		lines = lines || (!lay.inline && len(items) > 0)
	}
	return block(items, lines, indent), nil
}

// composed reports whether an element is written as a composite literal, not a name or a
// call of names and numbers.
func composed(text string) bool {
	return strings.ContainsAny(text, "{\n")
}

// block is items in braces: inline when they fit and none spans lines, else one a line.
func block(items []string, lines bool, indent int) string {
	if len(items) == 0 {
		return "{}"
	}
	inline := "{" + strings.Join(items, ", ") + "}"
	if !lines && fits(inline, indent) {
		return inline
	}
	return "{\n" + strings.Join(items, ",\n") + ",\n}"
}

// fits reports whether text is one line that fits from an indent.
func fits(text string, indent int) bool {
	return !strings.Contains(text, "\n") && indent+len(text) <= Columns
}

// perLine reports whether a list of t is written one element per line: strings, and
// anything composite.
func perLine(t reflect.Type) bool {
	switch t.Kind() {
	case reflect.String:
		return t.Name() == "string" || t.PkgPath() == ""
	case reflect.Struct:
		return t != pointType
	case reflect.Slice, reflect.Array, reflect.Map, reflect.Pointer, reflect.Interface:
		return true
	}
	return false
}

// local reports whether a type is declared in the file's package.
func (w *writer) local(t reflect.Type) bool {
	pkg, _, _ := strings.Cut(t.String(), ".")
	_, imported := w.imports[t.PkgPath()]
	return pkg == w.pkg && !imported
}

// omit reports whether a struct field is left out: a zero value, written plainly.
func (w *writer) omit(v reflect.Value, path string) bool {
	if s, ok := w.spelled[path]; ok && s.Kind != Plain {
		return false
	}
	return zero(v)
}

// zero reports whether a value is its type's zero value, an empty list or map counted so.
func zero(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Slice, reflect.Map:
		return v.Len() == 0
	}
	return v.IsZero()
}

// sortKeys sorts map keys: numbers by value, anything else by how it is written.
func sortKeys(keys []reflect.Value) {
	slices.SortFunc(keys, func(a, b reflect.Value) int {
		switch {
		case a.CanInt():
			return cmpOrdered(a.Int(), b.Int())
		case a.CanUint():
			return cmpOrdered(a.Uint(), b.Uint())
		case a.CanFloat():
			return cmpOrdered(a.Float(), b.Float())
		case a.Kind() == reflect.String:
			return strings.Compare(a.String(), b.String())
		}
		return strings.Compare(fmt.Sprint(interfaceOf(a)), fmt.Sprint(interfaceOf(b)))
	})
}

func cmpOrdered[T int64 | uint64 | float64](a, b T) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

// compositeOf is the composite literal an expression is, & or not.
func compositeOf(e ast.Expr) *ast.CompositeLit {
	if u, ok := e.(*ast.UnaryExpr); ok && u.Op.String() == "&" {
		e = u.X
	}
	lit, _ := e.(*ast.CompositeLit)
	return lit
}
