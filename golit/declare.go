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
	"strings"

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
// literal of a package var, or of the arguments of the call it is (slices.Concat(a, b)):
// key: value in a map, value alone in a slice or call, when key is nil. Both are written as
// an element's are, types elided, constants by name, in the style of the entries there; a
// Raw is written as it is. The entry takes its own line when the literal's entries do, and
// spans lines when the last one does. Packages it names are imported.
func AppendEntry(src []byte, name string, key, value any, sp Spellings) ([]byte, error) {
	f, err := Parse(src)
	if err != nil {
		return nil, err
	}
	e, ok := f.Var(name)
	if !ok {
		return nil, fmt.Errorf("no var %s", name)
	}
	if call, ok := e.(*ast.CallExpr); ok && key == nil {
		return appendArg(f, name, call, value, sp)
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
	values := make([]ast.Expr, len(lit.Elts))
	for i, elt := range lit.Elts {
		if kv, ok := elt.(*ast.KeyValueExpr); ok {
			elt = kv.Value
		}
		values[i] = elt
	}
	v := reflect.ValueOf(value)
	item, err := w.format(v, "", true, w.siblings(values, v.Type()), indent)
	if err != nil {
		return nil, err
	}
	keyText := ""
	if key != nil {
		if keyText, err = w.plain(reflect.ValueOf(key), "", true, layout{}, indent); err != nil {
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

// appendArg is a file's source with an argument added at the end of the call a var is.
func appendArg(f *File, name string, call *ast.CallExpr, value any, sp Spellings) ([]byte, error) {
	w := newWriter(f, "", sp)
	indent := w.indentAt(call.Rparen) + TabWidth
	text, err := w.format(reflect.ValueOf(value), "", false, layout{}, indent)
	if err != nil {
		return nil, err
	}
	var splice edit.Splice
	switch at := edit.Offset(f.Fset, call.Rparen); {
	case w.spansLines(call) && len(call.Args) > 0:
		splice = beforeBrace(f.Src, at, text+",\n", true)
	case len(call.Args) > 0:
		splice = edit.Insert(edit.Offset(f.Fset, call.Args[len(call.Args)-1].End()), ", "+text)
	default:
		splice = edit.Insert(at, text)
	}
	out, err := edit.Apply(f.Src, append([]edit.Splice{splice}, w.importSplices()...))
	if err != nil {
		return nil, err
	}
	back, err := Parse(out)
	if err != nil {
		return nil, err
	}
	be, _ := back.Var(name)
	bc, ok := be.(*ast.CallExpr)
	if !ok || len(bc.Args) != len(call.Args)+1 || normal(back.Source(bc.Args[len(bc.Args)-1])) != normal(text) {
		return nil, fmt.Errorf("the argument %s doesn't read back", text)
	}
	return out, nil
}

// AppendVar is a file's source declaring a new package var holding value, under a comment
// of doc's lines when doc isn't empty: in the file's last var block, or else after
// everything. It is written as Format writes a value, spelled as spelled says, and read
// back as it; a Raw is written as it is. Packages it names are imported.
func AppendVar[T any](src []byte, name, doc string, value T, sp Spellings, spelled map[string]Spelling) ([]byte, error) {
	f, err := Parse(src)
	if err != nil {
		return nil, err
	}
	if declared(f.AST, name) {
		return nil, fmt.Errorf("%s is already declared", name)
	}
	var block *ast.GenDecl
	for _, decl := range f.AST.Decls {
		if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.VAR && gen.Lparen.IsValid() {
			block = gen
		}
	}
	w := newWriter(f, "", sp)
	w.spelled = spelled
	indent := 0
	if block != nil {
		indent = TabWidth
	}
	text, err := w.format(reflect.ValueOf(&value).Elem(), "", false, layout{}, indent+len(name)+3)
	if err != nil {
		return nil, err
	}
	comment := ""
	if doc != "" {
		comment = "// " + strings.ReplaceAll(doc, "\n", "\n// ") + "\n"
	}
	var splice edit.Splice
	if block != nil {
		splice = beforeBrace(src, edit.Offset(f.Fset, block.Rparen), comment+name+" = "+text+"\n", false)
	} else {
		splice = edit.Insert(len(src), "\n"+comment+"var "+name+" = "+text+"\n")
	}
	out, err := edit.Apply(src, append([]edit.Splice{splice}, w.importSplices()...))
	if err != nil {
		return nil, err
	}
	if raw, ok := any(value).(Raw); ok {
		back, err := Parse(out)
		if err != nil {
			return nil, err
		}
		if e, ok := back.Var(name); !ok || normal(back.Source(e)) != normal(string(raw)) {
			return nil, fmt.Errorf("%s doesn't read back", name)
		}
		return out, nil
	}
	back, err := Read(out, name, value, sp)
	if err != nil {
		return nil, err
	}
	l := &Lit[T]{Spelled: map[string]Spelling{}}
	if err := l.readsAs(back, value, spelled); err != nil {
		return nil, fmt.Errorf("%s doesn't read back: %w", name, err)
	}
	return out, nil
}

// RemoveVar is a file's source without a package var's declaration, and the comment on it:
// its line in a var block, or its var declaration.
func RemoveVar(src []byte, name string) ([]byte, error) {
	f, err := Parse(src)
	if err != nil {
		return nil, err
	}
	for _, decl := range f.AST.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec)
			if len(vs.Names) != 1 || vs.Names[0].Name != name {
				continue
			}
			var n ast.Node = vs
			doc := vs.Doc
			if !gen.Lparen.IsValid() {
				n, doc = gen, gen.Doc
			}
			from := edit.Offset(f.Fset, n.Pos())
			if doc != nil {
				from = edit.Offset(f.Fset, doc.Pos())
			}
			from = bytes.LastIndexByte(src[:from], '\n') + 1
			to := len(src)
			if nl := bytes.IndexByte(src[edit.Offset(f.Fset, n.End()):], '\n'); nl >= 0 {
				to = edit.Offset(f.Fset, n.End()) + nl + 1
			}
			out, err := edit.Apply(src, []edit.Splice{{From: from, To: to}})
			if err != nil {
				return nil, err
			}
			if back, err := Parse(out); err != nil || declared(back.AST, name) {
				return nil, fmt.Errorf("%s is still declared", name)
			}
			return out, nil
		}
	}
	return nil, fmt.Errorf("no var %s of its own", name)
}
