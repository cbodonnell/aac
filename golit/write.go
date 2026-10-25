package golit

import (
	"bytes"
	"errors"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/printer"
	"go/token"
	"path"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/cbodonnell/aac/edit"
)

// Splices are the edits that turn the source of l into now.
//
// spelled gives how paths of now are spelled other than plainly; a Plain entry writes its
// path plainly. A path it leaves out keeps the source's spelling, a var's, a call's or
// opaque source, unless its value changed, and is otherwise written plainly, a constant by
// its name.
//
// origins gives, per list path of now, each element's index in the source's list, -1 for
// one added; a list it leaves out keeps its elements by index. Elements kept are edited in
// place, elements gone take their lines and comments, and elements added go after the
// kept element before them, or first. Kept elements in another order than the source's
// trade places, each with its own edits.
//
// A field that changes to its zero value goes; one that changes from it is added at the end of its literal. Map entries are matched by key, and
// new ones appended in sorted order. Packages written that the file doesn't import are
// imported.
func (l *Lit[T]) Splices(now T, spelled map[string]Spelling, origins map[string][]int) ([]edit.Splice, error) {
	w := newWriter(l.file, "", l.sp)
	w.spelled, w.origins, w.was = spelled, origins, l.Spelled
	e, ok := l.exprs[""]
	if !ok {
		return nil, errors.New("no literal read")
	}
	nv := reflect.ValueOf(&now).Elem()
	ov := reflect.ValueOf(&l.Value).Elem()
	if err := w.walk(nv, ov, "", "", e, w.indentAt(e.Pos())); err != nil {
		return nil, err
	}
	return append(w.splices, w.importSplices()...), nil
}

// Write is the source of l with now written in, formatted, and the literal read from it.
// The result must read as now, spelled as spelled says, or Write fails. l must have been
// read from a var.
func (l *Lit[T]) Write(now T, spelled map[string]Spelling, origins map[string][]int) ([]byte, *Lit[T], error) {
	if l.name == "" {
		return nil, nil, errors.New("only a var's literal is written back")
	}
	splices, err := l.Splices(now, spelled, origins)
	if err != nil {
		return nil, nil, err
	}
	out, err := edit.Apply(l.file.Src, splices)
	if err != nil {
		return nil, nil, fmt.Errorf("the source written doesn't parse: %w", err)
	}
	back, err := Read(out, l.name, now, l.sp)
	if err != nil {
		return nil, nil, err
	}
	if err := l.readsAs(back, now, spelled); err != nil {
		return nil, nil, fmt.Errorf("%s: the source written doesn't read back: %w", l.name, err)
	}
	return out, back, nil
}

// readsAs reports how a literal read back from a write isn't what was written: its value,
// the spellings asked for, and nothing opaque but what the source or spelled had.
func (l *Lit[T]) readsAs(back *Lit[T], now T, spelled map[string]Spelling) error {
	if p, ok := differ(reflect.ValueOf(&back.Value).Elem(), reflect.ValueOf(&now).Elem(), ""); ok {
		return fmt.Errorf("%s isn't the value written", orWhole(p))
	}
	for p, want := range spelled {
		if want.Kind == Plain {
			if got := back.Spelled[p]; got.Kind != Plain && got.Kind != Const {
				return fmt.Errorf("%s is spelled %s %s, not plainly", orWhole(p), got.Kind, got.Source)
			}
			continue
		}
		if got := back.Spelled[p]; !sameSpelling(got, want) {
			return fmt.Errorf("%s is spelled %s %q, not %s %q", orWhole(p), got.Kind, got.Source, want.Kind, want.Source)
		}
	}
	sources := map[string]bool{}
	for _, s := range l.Spelled {
		if s.Kind == Opaque {
			sources[normal(s.Source)] = true
		}
	}
	for _, p := range back.Opaque() {
		src := normal(back.Spelled[p].Source)
		if want, ok := spelled[p]; (ok && want.Kind == Opaque && normal(want.Source) == src) || sources[src] {
			continue
		}
		return fmt.Errorf("%s reads as opaque %s", orWhole(p), back.Spelled[p].Source)
	}
	return nil
}

func orWhole(p string) string {
	if p == "" {
		return "the whole"
	}
	return p
}

// normal is an expression's source as gofmt prints it.
func normal(src string) string {
	e, err := parser.ParseExpr(src)
	if err != nil {
		return src
	}
	var b bytes.Buffer
	printer.Fprint(&b, token.NewFileSet(), e)
	return b.String()
}

// sameSpelling reports whether two spellings write the same: opaque source as any
// spelling with that source, or else the same constant, var or call.
func sameSpelling(a, b Spelling) bool {
	if a.Kind == Opaque || b.Kind == Opaque {
		return a.Source != "" && normal(a.Source) == normal(b.Source)
	}
	if a.Kind != b.Kind {
		return false
	}
	switch a.Kind {
	case Const, Var:
		return a.Name == b.Name
	case Call:
		return a.Name == b.Name && reflect.DeepEqual(a.Args, b.Args)
	case Opaque:
		return normal(a.Source) == normal(b.Source)
	}
	return true
}

// differ is the first path at which two values differ, and false when they don't: lists
// and maps nil or empty alike, and functions compared only by whether they are nil.
func differ(a, b reflect.Value, path string) (string, bool) {
	if a.Kind() != b.Kind() {
		return path, true
	}
	switch a.Kind() {
	case reflect.Func:
		return path, a.IsNil() != b.IsNil()
	case reflect.Pointer, reflect.Interface:
		if a.IsNil() || b.IsNil() {
			return path, a.IsNil() != b.IsNil()
		}
		if a.Kind() == reflect.Interface && a.Elem().Type() != b.Elem().Type() {
			return path, true
		}
		return differ(a.Elem(), b.Elem(), path)
	case reflect.Struct:
		for i := range a.NumField() {
			if p, ok := differ(fieldOf(a, i), fieldOf(b, i), join(path, a.Type().Field(i).Name)); ok {
				return p, true
			}
		}
		return "", false
	case reflect.Slice, reflect.Array:
		if a.Len() != b.Len() {
			return path, true
		}
		for i := range a.Len() {
			if p, ok := differ(a.Index(i), b.Index(i), index(path, strconv.Itoa(i))); ok {
				return p, true
			}
		}
		return "", false
	case reflect.Map:
		if a.Len() != b.Len() {
			return path, true
		}
		for _, k := range a.MapKeys() {
			bv := b.MapIndex(k)
			if !bv.IsValid() {
				return index(path, KeyPath(k, false)), true
			}
			if p, ok := differ(a.MapIndex(k), bv, index(path, KeyPath(k, false))); ok {
				return p, true
			}
		}
		return "", false
	}
	return path, !a.Equal(b)
}

func equal(a, b reflect.Value) bool {
	_, ok := differ(a, b, "")
	return !ok
}

// walk writes now over the expression e that spells the source's old at src; path is
// now's.
func (w *writer) walk(now, old reflect.Value, path, src string, e ast.Expr, indent int) error {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			break
		}
		e = p.X
	}
	was := w.was[src]
	want, explicit := w.spelled[path]
	spelledOtherwise := was.Kind == Var || was.Kind == Call || was.Kind == Opaque
	switch {
	case explicit && want.Kind != Plain:
		if sameSpelling(want, was) {
			return nil
		}
		return w.replace(e, now, path, indent)
	case !explicit && spelledOtherwise:
		if equal(now, old) {
			return nil
		}
		return w.replace(e, now, path, indent)
	case spelledOtherwise:
		return w.replace(e, now, path, indent)
	}
	if lit := compositeOf(e); lit != nil {
		now, old := now, old
		for now.Kind() == reflect.Pointer || now.Kind() == reflect.Interface {
			if now.IsNil() || old.IsNil() || now.Elem().Type() != old.Elem().Type() {
				return w.replace(e, now, path, indent)
			}
			now, old = now.Elem(), old.Elem()
		}
		switch now.Kind() {
		case reflect.Struct:
			if now.Type() != pointType {
				return w.structLit(lit, now, old, path, src, indent)
			}
		case reflect.Slice, reflect.Array:
			return w.listLit(lit, now, old, path, src, indent)
		case reflect.Map:
			return w.mapLit(lit, now, old, path, src, indent)
		}
	}
	if equal(now, old) {
		return nil
	}
	return w.replace(e, now, path, indent)
}

// replace writes a value over an expression.
func (w *writer) replace(e ast.Expr, v reflect.Value, path string, indent int) error {
	text, err := w.format(v, path, w.elidedAt(e), false, indent)
	if err != nil {
		return err
	}
	if b, ok := e.(*ast.BasicLit); ok && b.Kind == token.CHAR && v.CanInt() {
		if _, explicit := w.spelled[path]; !explicit {
			text = strconv.QuoteRune(rune(v.Int()))
		}
	}
	w.splices = append(w.splices, edit.Replace(w.file.Fset, e, text))
	return nil
}

// elidedAt reports whether an expression leaves out its composite type, as an element of
// a list or map may.
func (w *writer) elidedAt(e ast.Expr) bool {
	if lit := compositeOf(e); lit != nil {
		if _, isAnd := e.(*ast.UnaryExpr); !isAnd && lit.Type == nil {
			return true
		}
	}
	return false
}

// structLit writes a struct over its literal.
func (w *writer) structLit(lit *ast.CompositeLit, now, old reflect.Value, path, src string, indent int) error {
	t := now.Type()
	if len(lit.Elts) > 0 {
		if _, keyed := lit.Elts[0].(*ast.KeyValueExpr); !keyed {
			for i, elt := range lit.Elts {
				name := t.Field(i).Name
				if err := w.walk(fieldOf(now, i), fieldOf(old, i), join(path, name), join(src, name), elt, indent+TabWidth); err != nil {
					return err
				}
			}
			return nil
		}
	}
	kvs := map[string]*ast.KeyValueExpr{}
	for _, elt := range lit.Elts {
		kv := elt.(*ast.KeyValueExpr)
		kvs[kv.Key.(*ast.Ident).Name] = kv
	}
	var adds []string
	for i := range t.NumField() {
		sf := t.Field(i)
		nf, of := fieldOf(now, i), fieldOf(old, i)
		fpath, fsrc := join(path, sf.Name), join(src, sf.Name)
		kv := kvs[sf.Name]
		switch {
		case kv != nil && w.omit(nf, fpath):
			// What the source writes for a value unchanged stays, a zero or not.
			_, explicit := w.spelled[fpath]
			if equal(nf, of) && (!explicit || w.was[fsrc].Kind == Plain || w.was[fsrc].Kind == Const) {
				continue
			}
			w.splices = append(w.splices, w.remove(kv))
		case kv != nil:
			if err := w.walk(nf, of, fpath, fsrc, kv.Value, indent+TabWidth); err != nil {
				return err
			}
		case !w.omit(nf, fpath):
			if !sf.IsExported() && !w.local(t) {
				return fmt.Errorf("%s: unexported outside its package", fpath)
			}
			text, err := w.format(nf, fpath, false, false, indent+TabWidth)
			if err != nil {
				return err
			}
			adds = append(adds, sf.Name+": "+text)
		}
	}
	if len(adds) == 0 {
		return nil
	}
	var single, multi []string
	for _, a := range adds {
		if strings.Contains(a, "\n") {
			multi = append(multi, a)
		} else {
			single = append(single, a)
		}
	}
	if !w.spansLines(lit) || len(lit.Elts) == 0 {
		w.splices = append(w.splices, w.insert(lit, nil, slices.Concat(single, multi), false)...)
		return nil
	}
	if len(single) > 0 {
		w.splices = append(w.splices, w.insert(lit, nil, single, true)...)
	}
	if len(multi) > 0 {
		w.splices = append(w.splices, w.afterLists(lit, multi))
	}
	return nil
}

// listLit writes a list over its literal.
func (w *writer) listLit(lit *ast.CompositeLit, now, old reflect.Value, path, src string, indent int) error {
	elts := lit.Elts
	for _, e := range elts {
		if _, ok := e.(*ast.KeyValueExpr); ok {
			return w.replace(lit, now, path, indent)
		}
	}
	n := now.Len()
	ord, ok := w.origins[path]
	if !ok {
		ord = make([]int, n)
		for i := range ord {
			ord[i] = i
			if i >= len(elts) {
				ord[i] = -1
			}
		}
	}
	if len(ord) != n {
		return fmt.Errorf("%s: %d origins for %d elements", orWhole(path), len(ord), n)
	}
	used := make([]bool, len(elts))
	var kept []int
	for k, o := range ord {
		if o < 0 {
			continue
		}
		if o >= len(elts) || used[o] {
			return fmt.Errorf("%s: element %d's origin %d isn't one of the source's %d", orWhole(path), k, o, len(elts))
		}
		used[o] = true
		kept = append(kept, k)
	}
	slots := make([]int, len(kept))
	for j, k := range kept {
		slots[j] = ord[k]
	}
	sort.Ints(slots)
	elemIndent := indent
	if w.spansLines(lit) {
		elemIndent += TabWidth
	}
	for j, k := range kept {
		o := ord[k]
		if slots[j] == o {
			if err := w.walk(now.Index(k), old.Index(o), index(path, strconv.Itoa(k)), index(src, strconv.Itoa(o)), elts[o], elemIndent); err != nil {
				return err
			}
			continue
		}
		text, err := w.moved(now.Index(k), old.Index(o), index(path, strconv.Itoa(k)), index(src, strconv.Itoa(o)), elts[o], elemIndent)
		if err != nil {
			return err
		}
		w.splices = append(w.splices, edit.Replace(w.file.Fset, elts[slots[j]], text))
	}
	for i, e := range elts {
		if !used[i] {
			w.splices = append(w.splices, w.remove(e))
		}
	}
	lines := len(elts) > 0 && w.spansLines(elts[len(elts)-1])
	perLine := w.spansLines(lit) || (len(elts) == 0 && perLine(now.Type().Elem()))
	// Elements added go before the kept element after them, in order.
	var anchor ast.Expr
	var items []string
	flush := func() {
		if len(items) > 0 {
			w.splices = append(w.splices, w.insert(lit, anchor, items, perLine)...)
		}
		items = nil
	}
	j := len(kept)
	for k := n - 1; k >= 0; k-- {
		if ord[k] >= 0 {
			flush()
			j--
			anchor = elts[slots[j]]
			continue
		}
		text, err := w.format(now.Index(k), index(path, strconv.Itoa(k)), true, lines, elemIndent)
		if err != nil {
			return err
		}
		items = append([]string{text}, items...)
	}
	flush()
	return nil
}

// moved is the source of a kept element with its edits made, to write in another's place.
func (w *writer) moved(now, old reflect.Value, path, src string, e ast.Expr, indent int) (string, error) {
	outer := w.splices
	w.splices = nil
	err := w.walk(now, old, path, src, e, indent)
	inner := w.splices
	w.splices = outer
	if err != nil {
		return "", err
	}
	from, to := edit.Offset(w.file.Fset, e.Pos()), edit.Offset(w.file.Fset, e.End())
	text := []byte(string(w.file.Src[from:to]))
	sort.SliceStable(inner, func(i, j int) bool { return inner[i].From > inner[j].From })
	for _, s := range inner {
		if s.From < from || s.To > to {
			return "", fmt.Errorf("%s: an edit reaches outside the element moved", orWhole(path))
		}
		text = slices.Concat(text[:s.From-from], []byte(s.Text), text[s.To-from:])
	}
	return string(text), nil
}

// mapLit writes a map over its literal.
func (w *writer) mapLit(lit *ast.CompositeLit, now, old reflect.Value, path, src string, indent int) error {
	d := &decoder{file: w.file, sp: w.sp}
	type entry struct {
		kv  *ast.KeyValueExpr
		key reflect.Value
	}
	have := map[string]entry{}
	char := false
	for _, elt := range lit.Elts {
		kv, ok := elt.(*ast.KeyValueExpr)
		if !ok {
			return w.replace(lit, now, path, indent)
		}
		key, kp, ok := d.key(kv.Key, now.Type().Key())
		if !ok {
			return w.replace(lit, now, path, indent)
		}
		if b, ok := kv.Key.(*ast.BasicLit); ok && b.Kind == token.CHAR {
			char = true
		}
		have[kp] = entry{kv, key}
	}
	elemIndent := indent
	if w.spansLines(lit) {
		elemIndent += TabWidth
	}
	keys := now.MapKeys()
	sortKeys(keys)
	seen := map[string]bool{}
	var adds []string
	lines := len(lit.Elts) > 0 && w.spansLines(lit.Elts[len(lit.Elts)-1].(*ast.KeyValueExpr).Value)
	for _, k := range keys {
		kp := KeyPath(k, char)
		p := index(path, kp)
		if e, ok := have[kp]; ok {
			seen[kp] = true
			if err := w.walk(now.MapIndex(k), old.MapIndex(e.key), p, index(src, kp), e.kv.Value, elemIndent); err != nil {
				return err
			}
			continue
		}
		key, err := w.plain(k, "", true, false, elemIndent)
		if err != nil {
			return err
		}
		if char && k.CanInt() {
			key = strconv.QuoteRune(rune(k.Int()))
		}
		text, err := w.format(now.MapIndex(k), p, true, lines, elemIndent)
		if err != nil {
			return err
		}
		adds = append(adds, key+": "+text)
	}
	for _, elt := range lit.Elts {
		kv := elt.(*ast.KeyValueExpr)
		_, kp, _ := d.key(kv.Key, now.Type().Key())
		if !seen[kp] {
			w.splices = append(w.splices, w.remove(kv))
		}
	}
	if len(adds) > 0 {
		w.splices = append(w.splices, w.insert(lit, nil, adds, w.spansLines(lit) || len(lit.Elts) == 0)...)
	}
	return nil
}

// spansLines reports whether a node starts and ends on different lines.
func (w *writer) spansLines(n ast.Node) bool {
	return w.file.Fset.Position(n.Pos()).Line != w.file.Fset.Position(n.End()).Line
}

// indentAt is the columns a line's indentation takes.
func (w *writer) indentAt(p token.Pos) int {
	at := edit.Offset(w.file.Fset, p)
	start := bytes.LastIndexByte(w.file.Src[:at], '\n') + 1
	n := 0
	for _, c := range w.file.Src[start:at] {
		switch c {
		case '\t':
			n += TabWidth
		case ' ':
			n++
		default:
			return n
		}
	}
	return n
}

// insert adds items to a literal: before anchor, or at the end when it is nil; one a line,
// or inline.
func (w *writer) insert(lit *ast.CompositeLit, anchor ast.Expr, items []string, perLine bool) []edit.Splice {
	fset, data := w.file.Fset, w.file.Src
	if perLine {
		text := strings.Join(items, ",\n") + ",\n"
		if anchor != nil {
			at := edit.Offset(fset, anchor.Pos())
			return []edit.Splice{edit.Insert(bytes.LastIndexByte(data[:at], '\n')+1, text)}
		}
		if len(lit.Elts) == 0 && !w.spansLines(lit) {
			return []edit.Splice{edit.Insert(edit.Offset(fset, lit.Lbrace)+1, "\n"+text)}
		}
		return []edit.Splice{beforeBrace(data, edit.Offset(fset, lit.Rbrace), text, len(lit.Elts) > 0)}
	}
	text := strings.Join(items, ", ")
	if anchor != nil {
		return []edit.Splice{edit.Insert(edit.Offset(fset, anchor.Pos()), text+", ")}
	}
	if last := w.lastKept(lit); last != nil {
		return []edit.Splice{edit.Insert(edit.Offset(fset, last.End()), ", "+text)}
	}
	return []edit.Splice{edit.Insert(edit.Offset(fset, lit.Lbrace)+1, text)}
}

// lastKept is the last element of a literal that no splice so far removes.
func (w *writer) lastKept(lit *ast.CompositeLit) ast.Expr {
	for i := len(lit.Elts) - 1; i >= 0; i-- {
		e := lit.Elts[i]
		from := edit.Offset(w.file.Fset, e.Pos())
		gone := slices.ContainsFunc(w.splices, func(s edit.Splice) bool { return s.Text == "" && s.From <= from && s.To > from })
		if !gone {
			return e
		}
	}
	return nil
}

// afterLists inserts items into a literal after its last element that spans lines, so
// gofmt doesn't align the elements around them.
func (w *writer) afterLists(lit *ast.CompositeLit, items []string) edit.Splice {
	text := strings.Join(items, ",\n") + ",\n"
	for i := len(lit.Elts) - 1; i >= 0; i-- {
		e := lit.Elts[i]
		if !w.spansLines(e) {
			continue
		}
		if nl := bytes.IndexByte(w.file.Src[edit.Offset(w.file.Fset, e.End()):], '\n'); nl >= 0 {
			return edit.Insert(edit.Offset(w.file.Fset, e.End())+nl+1, text)
		}
	}
	return beforeBrace(w.file.Src, edit.Offset(w.file.Fset, lit.Rbrace), text, len(lit.Elts) > 0)
}

// beforeBrace inserts lines before a closing brace at an offset: at the start of its line
// when it has the line to itself, else on a line of their own after what is before it,
// with a comma between when comma says.
func beforeBrace(data []byte, at int, lines string, comma bool) edit.Splice {
	start := bytes.LastIndexByte(data[:at], '\n') + 1
	if strings.TrimSpace(string(data[start:at])) == "" {
		return edit.Insert(start, lines)
	}
	if comma && !strings.HasSuffix(strings.TrimSpace(string(data[start:at])), ",") {
		return edit.Insert(at, ",\n"+lines)
	}
	return edit.Insert(at, "\n"+lines)
}

// remove removes an element of a literal with its comma, and its lines when it has them
// to itself, end-of-line comment and all. A comment on a line of its own before it stays.
func (w *writer) remove(n ast.Node) edit.Splice {
	data := w.file.Src
	from, to := edit.Offset(w.file.Fset, n.Pos()), edit.Offset(w.file.Fset, n.End())
	if to < len(data) && data[to] == ',' {
		to++
	}
	start := bytes.LastIndexByte(data[:from], '\n') + 1
	end := bytes.IndexByte(data[to:], '\n')
	if end < 0 {
		end = len(data)
	} else {
		end += to
	}
	rest := strings.TrimSpace(string(data[to:end]))
	if strings.TrimSpace(string(data[start:from])) == "" && (rest == "" || strings.HasPrefix(rest, "//")) {
		return edit.Splice{From: start, To: min(end+1, len(data))}
	}
	return edit.Splice{From: from, To: to}
}

// importSplices import the packages written that the file doesn't.
func (w *writer) importSplices() []edit.Splice {
	if len(w.used) == 0 {
		return nil
	}
	var lines []string
	for _, p := range slices.Sorted(mapKeys(w.used)) {
		spec := strconv.Quote(p)
		if name := w.used[p]; name != path.Base(p) {
			spec = name + " " + spec
		}
		lines = append(lines, spec)
	}
	return []edit.Splice{importSplice(w.file, lines)}
}

// importSplice adds import specs to a file: into its last import block, after its last
// import, or after its package clause.
func importSplice(f *File, specs []string) edit.Splice {
	var last *ast.GenDecl
	for _, decl := range f.AST.Decls {
		if gen, ok := decl.(*ast.GenDecl); ok && gen.Tok == token.IMPORT {
			last = gen
		}
	}
	switch {
	case last != nil && last.Lparen.IsValid():
		return beforeBrace(f.Src, edit.Offset(f.Fset, last.Rparen), strings.Join(specs, "\n")+"\n", false)
	case last != nil:
		return edit.Insert(edit.Offset(f.Fset, last.End()), "\nimport "+strings.Join(specs, "\nimport "))
	}
	return edit.Insert(edit.Offset(f.Fset, f.AST.Name.End()), "\n\nimport "+strings.Join(specs, "\nimport "))
}

// AddImports is a file's source importing packages, by path, with the names they are used
// by, that it doesn't yet.
func AddImports(src []byte, imports map[string]string) ([]byte, error) {
	f, err := Parse(src)
	if err != nil {
		return nil, err
	}
	w := newWriter(f, "", Spellings{})
	for p, name := range imports {
		if _, ok := w.imports[p]; !ok {
			w.used[p] = name
		}
	}
	out, err := edit.Apply(src, w.importSplices())
	if err != nil {
		return nil, err
	}
	return format.Source(out)
}
