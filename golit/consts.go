package golit

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
)

// Consts are a named type's constants, read from its package's source.
type Consts struct {
	Type reflect.Type
	// Pkg is the package's name, which qualifies them outside it.
	Pkg    string
	names  []string
	values map[string]reflect.Value
}

// Names are the constants' names, in the order declared.
func (c Consts) Names() []string {
	return slices.Clone(c.names)
}

// Value is the value of a constant, a value of Type.
func (c Consts) Value(name string) (any, bool) {
	v, ok := c.values[name]
	if !ok {
		return nil, false
	}
	return v.Interface(), true
}

// Name is the first constant declared with a value, and false when none has it.
func (c Consts) Name(v any) (string, bool) {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() || !rv.Type().ConvertibleTo(c.Type) {
		return "", false
	}
	rv = rv.Convert(c.Type)
	for _, name := range c.names {
		if c.values[name].Equal(rv) {
			return name, true
		}
	}
	return "", false
}

// ConstsIn reads a type's constants from the package in dir, its tests left out.
func ConstsIn(dir string, t reflect.Type) (Consts, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return Consts{}, err
	}
	var srcs [][]byte
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return Consts{}, err
		}
		srcs = append(srcs, data)
	}
	c, err := ReadConsts(t, srcs...)
	if err != nil {
		return c, fmt.Errorf("%s: %w", dir, err)
	}
	return c, nil
}

// ReadConsts reads a type's constants from its package's files. A constant is of the type
// when its spec names it (Name T = …) or repeats a spec that does, as Go counts through a
// const block, and its value is an expression of literals, iota and the package's other
// constants.
func ReadConsts(t reflect.Type, srcs ...[]byte) (Consts, error) {
	c := Consts{Type: t, values: map[string]reflect.Value{}}
	type spec struct {
		names []string
		exprs []ast.Expr
		iota  int
		typed bool
	}
	var specs []spec
	for _, src := range srcs {
		f, err := parser.ParseFile(token.NewFileSet(), "", src, parser.SkipObjectResolution)
		if err != nil {
			return c, err
		}
		if c.Pkg == "" {
			c.Pkg = f.Name.Name
		}
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.CONST {
				continue
			}
			var exprs []ast.Expr
			typed := false
			for iota, s := range gen.Specs {
				vs := s.(*ast.ValueSpec)
				if len(vs.Values) > 0 {
					exprs = vs.Values
					_, name, _ := qualified(vs.Type)
					typed = vs.Type != nil && name == t.Name()
				}
				sp := spec{exprs: exprs, iota: iota, typed: typed}
				for _, n := range vs.Names {
					sp.names = append(sp.names, n.Name)
				}
				specs = append(specs, sp)
			}
		}
	}
	// Constants can refer to ones declared later, or in another file: each pass values
	// what it can.
	known := map[string]constant.Value{}
	for progress := true; progress; {
		progress = false
		for _, s := range specs {
			for i, name := range s.names {
				if _, ok := known[name]; ok || name == "_" || i >= len(s.exprs) {
					continue
				}
				if v, ok := evaluate(s.exprs[i], s.iota, known); ok {
					known[name] = v
					progress = true
				}
			}
		}
	}
	for _, s := range specs {
		if !s.typed {
			continue
		}
		for _, name := range s.names {
			k, ok := known[name]
			if !ok || name == "_" {
				continue
			}
			v := reflect.New(t).Elem()
			if !setConstant(v, k) {
				continue
			}
			c.names = append(c.names, name)
			c.values[name] = v
		}
	}
	if len(c.names) == 0 {
		return c, fmt.Errorf("no constants of %s", t.Name())
	}
	return c, nil
}

// evaluate is a constant expression's value.
func evaluate(e ast.Expr, iota int, known map[string]constant.Value) (v constant.Value, ok bool) {
	// go/constant panics on an operation its operands don't allow, as a comparison.
	defer func() {
		if recover() != nil {
			v, ok = nil, false
		}
	}()
	switch e := e.(type) {
	case *ast.BasicLit:
		v := constant.MakeFromLiteral(e.Value, e.Kind, 0)
		return v, v.Kind() != constant.Unknown
	case *ast.Ident:
		switch e.Name {
		case "iota":
			return constant.MakeInt64(int64(iota)), true
		case "true", "false":
			return constant.MakeBool(e.Name == "true"), true
		}
		v, ok := known[e.Name]
		return v, ok
	case *ast.ParenExpr:
		return evaluate(e.X, iota, known)
	case *ast.UnaryExpr:
		x, ok := evaluate(e.X, iota, known)
		if !ok {
			return nil, false
		}
		v := constant.UnaryOp(e.Op, x, 0)
		return v, v.Kind() != constant.Unknown
	case *ast.BinaryExpr:
		x, ok := evaluate(e.X, iota, known)
		y, ok2 := evaluate(e.Y, iota, known)
		if !ok || !ok2 {
			return nil, false
		}
		if e.Op == token.SHL || e.Op == token.SHR {
			n, exact := constant.Uint64Val(y)
			if !exact {
				return nil, false
			}
			v := constant.Shift(x, e.Op, uint(n))
			return v, v.Kind() != constant.Unknown
		}
		if x.Kind() == constant.Unknown || y.Kind() == constant.Unknown {
			return nil, false
		}
		if e.Op == token.QUO && x.Kind() == constant.Int && y.Kind() == constant.Int {
			return constant.BinaryOp(x, token.QUO_ASSIGN, y), true
		}
		v := constant.BinaryOp(x, e.Op, y)
		return v, v.Kind() != constant.Unknown
	case *ast.CallExpr:
		// A conversion, T(x), keeps x's value.
		if len(e.Args) == 1 {
			return evaluate(e.Args[0], iota, known)
		}
	}
	return nil, false
}
