package golit

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"reflect"
	"strconv"

	"github.com/cbodonnell/aac/edit"
)

// AppendConst is a file's source with a constant of a named type added after the type's
// last: named alone when that one ends its block and counts by iota, so it takes the next
// value, or else given the type and one more than the largest of the type's values in the
// file. Every other constant keeps its value.
func AppendConst(src []byte, typ, name string) ([]byte, error) {
	f, err := Parse(src)
	if err != nil {
		return nil, err
	}
	if declared(f.AST, name) {
		return nil, fmt.Errorf("%s is already declared", name)
	}
	specs := fileConstSpecs(f.AST)
	before := constValues(specs)
	last := -1
	for i, s := range specs {
		if s.typ == typ {
			last = i
		}
	}
	if last < 0 {
		return nil, fmt.Errorf("no constants of %s", typ)
	}
	s := specs[last]
	text := name
	if !(usesIota(s.exprs) && s.at == len(s.block.Specs)-1) {
		next := constant.MakeInt64(0)
		for _, sp := range specs {
			if sp.typ != typ {
				continue
			}
			for _, n := range sp.names {
				v, ok := before[n]
				if !ok || v.Kind() != constant.Int {
					return nil, fmt.Errorf("%s isn't a whole number, so %s can't be numbered after it", n, name)
				}
				if constant.Compare(v, token.GEQ, next) {
					next = constant.BinaryOp(v, token.ADD, constant.MakeInt64(1))
				}
			}
		}
		text = name + " " + typ + " = " + next.ExactString()
	}
	spec := s.block.Specs[s.at]
	at := edit.Offset(f.Fset, spec.End())
	if nl := bytes.IndexByte(src[at:], '\n'); nl >= 0 {
		at += nl
	} else {
		at = len(src)
	}
	if s.block.Lparen.IsValid() {
		text = "\n" + text
	} else {
		text = "\nconst " + text
	}
	out, err := edit.Apply(src, []edit.Splice{edit.Insert(at, text)})
	if err != nil {
		return nil, err
	}
	// Read back: the others keep their values, and the new one has one no other has.
	back, err := Parse(out)
	if err != nil {
		return nil, err
	}
	backSpecs := fileConstSpecs(back.AST)
	after := constValues(backSpecs)
	for n, v := range before {
		if w, ok := after[n]; !ok || !constant.Compare(v, token.EQL, w) {
			return nil, fmt.Errorf("adding %s changed %s", name, n)
		}
	}
	v, ok := after[name]
	if !ok {
		return nil, fmt.Errorf("%s doesn't read back", name)
	}
	for _, sp := range backSpecs {
		for _, n := range sp.names {
			if sp.typ == typ && n != name && constant.Compare(after[n], token.EQL, v) {
				return nil, fmt.Errorf("%s reads back as %s, %s's value", name, v, n)
			}
			if n == name && sp.typ != typ {
				return nil, fmt.Errorf("%s doesn't read back as a %s", name, typ)
			}
		}
	}
	return out, nil
}

// declared reports whether a file declares a name at its top level.
func declared(f *ast.File, name string) bool {
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil && d.Name.Name == name {
				return true
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				switch s := spec.(type) {
				case *ast.ValueSpec:
					for _, n := range s.Names {
						if n.Name == name {
							return true
						}
					}
				case *ast.TypeSpec:
					if s.Name.Name == name {
						return true
					}
				}
			}
		}
	}
	return false
}

func usesIota(exprs []ast.Expr) bool {
	found := false
	for _, e := range exprs {
		ast.Inspect(e, func(n ast.Node) bool {
			if id, ok := n.(*ast.Ident); ok && id.Name == "iota" {
				found = true
			}
			return !found
		})
	}
	return found
}

// AppendEntry is a file's source with an entry added at the end of the map or slice
// literal of a package var: key: value in a map, value alone in a slice, when key is nil.
// Both are written as an element's are, types elided, constants by name; a Raw is written
// as it is. The entry takes its own line when the literal's entries do, and spans lines
// when the last one does. Packages it names are imported.
func AppendEntry(src []byte, name string, key, value any, sp Spellings) ([]byte, error) {
	f, err := Parse(src)
	if err != nil {
		return nil, err
	}
	e, ok := f.Var(name)
	if !ok {
		return nil, fmt.Errorf("no var %s", name)
	}
	lit := compositeOf(e)
	if lit == nil {
		return nil, fmt.Errorf("%s isn't a composite literal", name)
	}
	w := newWriter(f, "", sp)
	indent := w.indentAt(lit.Pos())
	if w.spansLines(lit) {
		indent = w.indentAt(lit.Rbrace) + TabWidth
	}
	lines := false
	if n := len(lit.Elts); n > 0 {
		last := lit.Elts[n-1]
		if kv, ok := last.(*ast.KeyValueExpr); ok {
			last = kv.Value
		}
		lines = w.spansLines(last)
	}
	item, err := w.format(reflect.ValueOf(value), "", true, lines, indent)
	if err != nil {
		return nil, err
	}
	keyText := ""
	if key != nil {
		if keyText, err = w.plain(reflect.ValueOf(key), "", true, false, indent); err != nil {
			return nil, err
		}
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				return nil, fmt.Errorf("%s isn't keyed", name)
			}
			if normal(f.Source(kv.Key)) == normal(keyText) {
				return nil, fmt.Errorf("%s already has %s", name, keyText)
			}
		}
		item = keyText + ": " + item
	}
	splices := w.insert(lit, nil, []string{item}, w.spansLines(lit) || len(lit.Elts) == 0)
	out, err := edit.Apply(src, append(splices, w.importSplices()...))
	if err != nil {
		return nil, err
	}
	back, err := Parse(out)
	if err != nil {
		return nil, err
	}
	be, _ := back.Var(name)
	bl := compositeOf(be)
	if bl == nil || len(bl.Elts) != len(lit.Elts)+1 {
		return nil, errors.New("the entry doesn't read back")
	}
	last := bl.Elts[len(bl.Elts)-1]
	if kv, ok := last.(*ast.KeyValueExpr); key != nil && (!ok || normal(back.Source(kv.Key)) != normal(keyText)) {
		return nil, fmt.Errorf("the entry %s doesn't read back", strconv.Quote(keyText))
	}
	return out, nil
}
