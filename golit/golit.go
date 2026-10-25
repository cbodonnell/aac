// Package golit is a codec for Go composite literals: it reads the literal a package var
// holds into a value of its type by reflection, keeping how each part was spelled (a
// constant's name, a var's, a helper call), so an editor can edit the value and write it
// back.
package golit

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"maps"
	"reflect"
	"slices"
	"strings"
)

// Kind is how a value is spelled.
type Kind int

const (
	// Plain is a value spelled as the codec writes it by itself: basic literals,
	// composite literals of them, image.Pt(x, y).
	Plain Kind = iota
	// Const is a named type's constant, or constants of it joined by | (flags).
	Const
	// Var is a registered package var.
	Var
	// Call is a call of a registered helper.
	Call
	// Opaque is anything else: its source is kept as written, and its value is the build's.
	Opaque
)

func (k Kind) String() string {
	switch k {
	case Plain:
		return "plain"
	case Const:
		return "const"
	case Var:
		return "var"
	case Call:
		return "call"
	case Opaque:
		return "opaque"
	}
	return fmt.Sprintf("Kind(%d)", int(k))
}

// Spelling is how a value other than a plain one is written.
type Spelling struct {
	Kind Kind
	// Name is the constant's, var's or helper's name, unqualified; constants joined by |
	// are named so ("FlipD | FlipH").
	Name string
	// Args are a call's arguments, decoded, a variadic call's each one.
	Args []any
	// Source is the expression as written, gofmt'ed.
	Source string
}

// Lit is a decoded literal: its value, and how each path was spelled.
type Lit[T any] struct {
	Value T
	// Spelled maps paths ("Props[3].Stage", "Keys['o']", "" for the whole) to a spelling
	// other than the plain one. A helper call's arguments are at its path with "(i)" on
	// the end.
	Spelled map[string]Spelling
	file    *File
	exprs   map[string]ast.Expr
}

// Opaque are the paths whose values aren't spelled in a way the codec reads, sorted. Their
// values are the build's.
func (l *Lit[T]) Opaque() []string {
	var out []string
	for path, s := range l.Spelled {
		if s.Kind == Opaque {
			out = append(out, path)
		}
	}
	slices.Sort(out)
	return out
}

// Paths are the paths the source spells, sorted: every field, element and map entry
// written, and the whole as "".
func (l *Lit[T]) Paths() []string {
	return slices.Sorted(maps.Keys(l.exprs))
}

// Written reports whether the source spells a path.
func (l *Lit[T]) Written(path string) bool {
	_, ok := l.exprs[path]
	return ok
}

// Source is a path's expression as written, gofmt'ed, "" when the source doesn't spell it.
func (l *Lit[T]) Source(path string) string {
	e, ok := l.exprs[path]
	if !ok {
		return ""
	}
	return l.file.Source(e)
}

// Spellings say how values the codec can't spell alone are written.
type Spellings struct {
	// Consts are named types' constants, by type.
	Consts map[reflect.Type]Consts
	// Vars are package vars by the name they are spelled by, with their values.
	Vars map[string]any
	// Helpers are functions by the name they are called by.
	Helpers map[string]Helper
	// Types are the concrete types an interface may hold as a composite literal, by the
	// name the literal is spelled with.
	Types map[string]reflect.Type
}

// Helper is a function a literal calls to make a value.
type Helper struct {
	Func any
	// Label is what an editor shows it as, and Args name its arguments.
	Label string
	Args  []string
}

// Call makes a helper's value from its arguments, for an editor that changed one.
func (sp Spellings) Call(helper string, args []any) (any, error) {
	h, ok := sp.Helpers[helper]
	if !ok {
		return nil, fmt.Errorf("no helper %s", helper)
	}
	fn := reflect.ValueOf(h.Func)
	ft := fn.Type()
	if n := ft.NumIn(); len(args) < n-1 || (!ft.IsVariadic() && len(args) != n) {
		return nil, fmt.Errorf("%s takes %d arguments, not %d", helper, n, len(args))
	}
	in := make([]reflect.Value, len(args))
	for i, a := range args {
		t := paramType(ft, i)
		v := reflect.ValueOf(a)
		if !v.IsValid() {
			v = reflect.Zero(t)
		}
		if !v.Type().ConvertibleTo(t) {
			return nil, fmt.Errorf("%s's argument %d is a %s, not a %s", helper, i+1, v.Type(), t)
		}
		in[i] = v.Convert(t)
	}
	out, err := call(fn, in)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", helper, err)
	}
	return out.Interface(), nil
}

// call calls a function for its one result, and fails if it panics.
func call(fn reflect.Value, in []reflect.Value) (out reflect.Value, err error) {
	if fn.Type().NumOut() == 0 {
		return out, fmt.Errorf("has no result")
	}
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%v", r)
		}
	}()
	return fn.Call(in)[0], nil
}

func paramType(ft reflect.Type, i int) reflect.Type {
	if ft.IsVariadic() && i >= ft.NumIn()-1 {
		return ft.In(ft.NumIn() - 1).Elem()
	}
	return ft.In(i)
}

// File is a parsed source file.
type File struct {
	Fset *token.FileSet
	AST  *ast.File
	Src  []byte
}

// Parse parses a source file, comments and all.
func Parse(src []byte) (*File, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "", src, parser.ParseComments|parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	return &File{Fset: fset, AST: f, Src: src}, nil
}

// Var is the expression a top-level var is declared with, and false when the file has no
// such var or it has no one value of its own.
func (f *File) Var(name string) (ast.Expr, bool) {
	for _, decl := range f.AST.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, n := range vs.Names {
				if n.Name == name && len(vs.Values) == len(vs.Names) {
					return vs.Values[i], true
				}
			}
		}
	}
	return nil, false
}

// Vars are the names of the file's top-level vars that each have a value, in the order
// declared.
func (f *File) Vars() []string {
	var out []string
	for _, decl := range f.AST.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec)
			if len(vs.Values) == len(vs.Names) {
				for _, n := range vs.Names {
					out = append(out, n.Name)
				}
			}
		}
	}
	return out
}

// Source is a node's source as gofmt prints it.
func (f *File) Source(n ast.Node) string {
	var b bytes.Buffer
	if err := printer.Fprint(&b, f.Fset, n); err != nil {
		return string(f.Src[f.Fset.Position(n.Pos()).Offset:f.Fset.Position(n.End()).Offset])
	}
	return b.String()
}

// Read decodes the literal of the package var name in src into a T. built is the value the
// build has for that var: what Read can't decode is taken from it, at the same path, and
// kept as Opaque.
func Read[T any](src []byte, name string, built T, sp Spellings) (*Lit[T], error) {
	f, err := Parse(src)
	if err != nil {
		return nil, err
	}
	return ReadVar(f, name, built, sp)
}

// ReadVar is Read of a parsed file.
func ReadVar[T any](f *File, name string, built T, sp Spellings) (*Lit[T], error) {
	e, ok := f.Var(name)
	if !ok {
		return nil, fmt.Errorf("no var %s in the source", name)
	}
	return ReadExpr(f, e, built, sp), nil
}

// ReadExpr decodes an expression of a parsed file into a T, as Read does a var's, for a
// literal no var holds alone (one passed to a function). A T that is an interface is read
// as the concrete type the literal names (Spellings.Types), or else as built's.
func ReadExpr[T any](f *File, e ast.Expr, built T, sp Spellings) *Lit[T] {
	l := &Lit[T]{Spelled: map[string]Spelling{}, file: f, exprs: map[string]ast.Expr{}}
	d := &decoder{file: f, sp: sp, spelled: l.Spelled, exprs: l.exprs}
	t := reflect.TypeFor[T]()
	b := reflect.ValueOf(&built).Elem()
	v := d.decode(e, t, b, "")
	reflect.ValueOf(&l.Value).Elem().Set(v)
	return l
}

// join is a field's path in a value at path.
func join(path, field string) string {
	if path == "" {
		return field
	}
	return path + "." + field
}

// index is an element's or entry's path in a list or map at path; key is written as
// Go spells it.
func index(path, key string) string {
	return path + "[" + key + "]"
}

// pkgName is the package a file declares.
func (f *File) pkgName() string {
	return f.AST.Name.Name
}

// qualified splits a spelled name into its package and name: "shop.ColorRed" is shop and
// ColorRed, "warm" is "" and warm.
func qualified(e ast.Expr) (pkg, name string, ok bool) {
	switch e := e.(type) {
	case *ast.Ident:
		return "", e.Name, true
	case *ast.SelectorExpr:
		if x, ok := e.X.(*ast.Ident); ok {
			return x.Name, e.Sel.Name, true
		}
	}
	return "", "", false
}

func spelledName(pkg, name string) string {
	if pkg == "" {
		return name
	}
	return pkg + "." + name
}

// typeName is the name a composite literal is spelled with: "Item", "shop.Item",
// "[]Item", "" when elided.
func (f *File) typeName(e ast.Expr) string {
	if e == nil {
		return ""
	}
	return strings.ReplaceAll(f.Source(e), " ", "")
}
