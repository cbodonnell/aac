package golit

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"image"
	"reflect"
	"strconv"
	"unsafe"
)

// decoder reads one literal, recording spellings and where each path is written.
type decoder struct {
	file    *File
	sp      Spellings
	spelled map[string]Spelling
	exprs   map[string]ast.Expr
}

var pointType = reflect.TypeFor[image.Point]()

// decode reads e into a value of t. built is the build's value at the same path, invalid
// when it has none, and is what an expression the codec can't read takes.
func (d *decoder) decode(e ast.Expr, t reflect.Type, built reflect.Value, path string) reflect.Value {
	for {
		p, ok := e.(*ast.ParenExpr)
		if !ok {
			break
		}
		e = p.X
	}
	d.exprs[path] = e
	if v, ok := d.spelledAs(e, t, path); ok {
		return v
	}
	if v, ok := d.plain(e, t, built, path); ok {
		return v
	}
	return d.opaque(e, t, built, path)
}

// opaque is the build's value for an expression the codec can't read, or the zero value
// when the build has none.
func (d *decoder) opaque(e ast.Expr, t reflect.Type, built reflect.Value, path string) reflect.Value {
	d.forget(path)
	d.exprs[path] = e
	d.spelled[path] = Spelling{Kind: Opaque, Source: d.file.Source(e)}
	v := reflect.New(t).Elem()
	if built.IsValid() && built.Type().AssignableTo(t) {
		v.Set(built)
	}
	return v
}

// forget drops what was recorded under a path, for one read again as a whole.
func (d *decoder) forget(path string) {
	for p := range d.exprs {
		if Under(p, path) {
			delete(d.exprs, p)
			delete(d.spelled, p)
		}
	}
}

// Under reports whether the path p is path or inside it: a field, element, entry or
// argument of it, at any depth.
func Under(p, path string) bool {
	if p == path || path == "" {
		return true
	}
	if len(p) <= len(path) || p[:len(path)] != path {
		return false
	}
	switch p[len(path)] {
	case '.', '[', '(':
		return true
	}
	return false
}

// spelledAs reads a constant, a var or a helper call.
func (d *decoder) spelledAs(e ast.Expr, t reflect.Type, path string) (reflect.Value, bool) {
	switch e := e.(type) {
	case *ast.Ident, *ast.SelectorExpr:
		pkg, name, _ := qualified(e)
		if pkg == "" && (name == "nil" || name == "true" || name == "false") {
			return reflect.Value{}, false
		}
		if v, ok := d.constant(pkg, name, t); ok {
			d.spelled[path] = Spelling{Kind: Const, Name: name, Source: d.file.Source(e)}
			return v, true
		}
		if val, ok := d.sp.Vars[spelledName(pkg, name)]; ok {
			if v, ok := convert(reflect.ValueOf(val), t); ok {
				d.spelled[path] = Spelling{Kind: Var, Name: spelledName(pkg, name), Source: d.file.Source(e)}
				return v, true
			}
		}
	case *ast.BinaryExpr:
		if e.Op != token.OR {
			break
		}
		names, v, ok := d.flags(e, t)
		if ok {
			d.spelled[path] = Spelling{Kind: Const, Name: names, Source: d.file.Source(e)}
			return v, true
		}
	case *ast.CallExpr:
		pkg, name, ok := qualified(e.Fun)
		if !ok {
			break
		}
		h, ok := d.sp.Helpers[spelledName(pkg, name)]
		if !ok || e.Ellipsis.IsValid() {
			break
		}
		fn := reflect.ValueOf(h.Func)
		ft := fn.Type()
		if n := ft.NumIn(); len(e.Args) < n-1 || (!ft.IsVariadic() && len(e.Args) != n) {
			break
		}
		in := make([]reflect.Value, len(e.Args))
		args := make([]any, len(e.Args))
		for i, a := range e.Args {
			at := path + "(" + strconv.Itoa(i) + ")"
			in[i] = d.decode(a, paramType(ft, i), reflect.Value{}, at)
			if d.spelled[at].Kind == Opaque {
				d.forget(path)
				return reflect.Value{}, false
			}
			args[i] = in[i].Interface()
		}
		out, err := call(fn, in)
		if err != nil {
			d.forget(path)
			return reflect.Value{}, false
		}
		v, ok := convert(out, t)
		if !ok {
			d.forget(path)
			return reflect.Value{}, false
		}
		d.exprs[path] = e
		d.spelled[path] = Spelling{Kind: Call, Name: spelledName(pkg, name), Args: args, Source: d.file.Source(e)}
		return v, true
	}
	return reflect.Value{}, false
}

// convert is a value as a t: assigned, converted between named and unnamed types of one
// kind, or held by an interface.
func convert(v reflect.Value, t reflect.Type) (reflect.Value, bool) {
	if !v.IsValid() {
		return reflect.Value{}, false
	}
	out := reflect.New(t).Elem()
	switch {
	case v.Type().AssignableTo(t):
		out.Set(v)
	case v.Kind() == t.Kind() && v.Type().ConvertibleTo(t):
		out.Set(v.Convert(t))
	default:
		return reflect.Value{}, false
	}
	return out, true
}

// constant is the value of a constant of t named name, qualified by pkg.
func (d *decoder) constant(pkg, name string, t reflect.Type) (reflect.Value, bool) {
	c, ok := d.sp.Consts[t]
	if !ok {
		return reflect.Value{}, false
	}
	// A constant is qualified outside its package, and only there.
	if (pkg == "") != (c.Pkg == d.file.pkgName()) || (pkg != "" && pkg != c.Pkg) {
		return reflect.Value{}, false
	}
	v, ok := c.values[name]
	return v, ok
}

// flags reads constants of t joined by |.
func (d *decoder) flags(e ast.Expr, t reflect.Type) (string, reflect.Value, bool) {
	switch k := t.Kind(); {
	case k >= reflect.Int && k <= reflect.Int64, k >= reflect.Uint && k <= reflect.Uintptr:
	default:
		return "", reflect.Value{}, false
	}
	if b, ok := e.(*ast.BinaryExpr); ok && b.Op == token.OR {
		ln, lv, ok := d.flags(b.X, t)
		rn, rv, ok2 := d.flags(b.Y, t)
		if !ok || !ok2 {
			return "", reflect.Value{}, false
		}
		v := reflect.New(t).Elem()
		if v.CanInt() {
			v.SetInt(lv.Int() | rv.Int())
		} else {
			v.SetUint(lv.Uint() | rv.Uint())
		}
		return ln + " | " + rn, v, true
	}
	pkg, name, ok := qualified(e)
	if !ok {
		return "", reflect.Value{}, false
	}
	v, ok := d.constant(pkg, name, t)
	return name, v, ok
}

// plain reads a basic literal, a composite literal or image.Pt(x, y).
func (d *decoder) plain(e ast.Expr, t reflect.Type, built reflect.Value, path string) (reflect.Value, bool) {
	v := reflect.New(t).Elem()
	switch e := e.(type) {
	case *ast.Ident:
		switch {
		case e.Name == "nil" && nilable(t.Kind()):
			return v, true
		case (e.Name == "true" || e.Name == "false") && t.Kind() == reflect.Bool:
			v.SetBool(e.Name == "true")
			return v, true
		}
	case *ast.BasicLit, *ast.UnaryExpr:
		if u, ok := e.(*ast.UnaryExpr); ok && u.Op == token.AND {
			if t.Kind() != reflect.Pointer {
				return v, false
			}
			lit, ok := u.X.(*ast.CompositeLit)
			if !ok {
				return v, false
			}
			return d.composite(lit, t, built, path)
		}
		c, ok := number(e)
		if !ok {
			return v, false
		}
		return v, setConstant(v, c)
	case *ast.CallExpr:
		if t != pointType || len(e.Args) != 2 {
			return v, false
		}
		if pkg, name, ok := qualified(e.Fun); !ok || pkg != "image" || name != "Pt" {
			return v, false
		}
		x, ok := number(e.Args[0])
		y, ok2 := number(e.Args[1])
		if !ok || !ok2 {
			return v, false
		}
		var p image.Point
		if !setConstant(reflect.ValueOf(&p.X).Elem(), x) || !setConstant(reflect.ValueOf(&p.Y).Elem(), y) {
			return v, false
		}
		v.Set(reflect.ValueOf(p))
		return v, true
	case *ast.CompositeLit:
		return d.composite(e, t, built, path)
	}
	return v, false
}

func nilable(k reflect.Kind) bool {
	switch k {
	case reflect.Pointer, reflect.Slice, reflect.Map, reflect.Func, reflect.Interface, reflect.Chan:
		return true
	}
	return false
}

// number reads a basic literal, negated or not.
func number(e ast.Expr) (constant.Value, bool) {
	neg := false
	if u, ok := e.(*ast.UnaryExpr); ok && (u.Op == token.SUB || u.Op == token.ADD) {
		neg, e = u.Op == token.SUB, u.X
	}
	b, ok := e.(*ast.BasicLit)
	if !ok {
		return nil, false
	}
	c := constant.MakeFromLiteral(b.Value, b.Kind, 0)
	if c.Kind() == constant.Unknown {
		return nil, false
	}
	if neg {
		if c.Kind() == constant.String {
			return nil, false
		}
		c = constant.UnaryOp(token.SUB, c, 0)
	}
	return c, true
}

// setConstant sets v to a constant, as Go would convert it: exactly, a whole number into
// an int, any number into a float, a string into a string.
func setConstant(v reflect.Value, c constant.Value) bool {
	switch k := v.Kind(); {
	case k == reflect.String:
		if c.Kind() != constant.String {
			return false
		}
		v.SetString(constant.StringVal(c))
	case k == reflect.Bool:
		if c.Kind() != constant.Bool {
			return false
		}
		v.SetBool(constant.BoolVal(c))
	case v.CanInt():
		n, exact := constant.Int64Val(constant.ToInt(c))
		if !exact || c.Kind() == constant.String || v.OverflowInt(n) {
			return false
		}
		v.SetInt(n)
	case v.CanUint():
		n, exact := constant.Uint64Val(constant.ToInt(c))
		if !exact || c.Kind() == constant.String || v.OverflowUint(n) {
			return false
		}
		v.SetUint(n)
	case v.CanFloat():
		if c.Kind() != constant.Int && c.Kind() != constant.Float {
			return false
		}
		f, _ := constant.Float64Val(c)
		v.SetFloat(f)
	default:
		return false
	}
	return true
}

// composite reads a composite literal: a struct by key or position, a slice or array, a
// map, a pointer to one with & elided or not, or an interface holding one.
func (d *decoder) composite(lit *ast.CompositeLit, t reflect.Type, built reflect.Value, path string) (reflect.Value, bool) {
	v := reflect.New(t).Elem()
	switch t.Kind() {
	case reflect.Interface:
		ct, ok := d.sp.Types[d.file.typeName(lit.Type)]
		var b reflect.Value
		if built.IsValid() && !built.IsNil() {
			b = built.Elem()
			if !ok {
				ct = b.Type()
			}
		}
		if ct == nil || !ct.Implements(t) {
			return v, false
		}
		cv, ok := d.composite(lit, ct, b, path)
		if !ok {
			return v, false
		}
		v.Set(cv)
		return v, true
	case reflect.Pointer:
		if t.Elem().Kind() != reflect.Struct {
			return v, false
		}
		var b reflect.Value
		if built.IsValid() && !built.IsNil() {
			b = built.Elem()
		}
		ev, ok := d.composite(lit, t.Elem(), b, path)
		if !ok {
			return v, false
		}
		p := reflect.New(t.Elem())
		p.Elem().Set(ev)
		v.Set(p)
		return v, true
	case reflect.Struct:
		if len(lit.Elts) > 0 {
			if _, keyed := lit.Elts[0].(*ast.KeyValueExpr); keyed {
				for _, elt := range lit.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						return v, false
					}
					key, ok := kv.Key.(*ast.Ident)
					if !ok {
						return v, false
					}
					sf, ok := t.FieldByName(key.Name)
					if !ok || len(sf.Index) != 1 {
						return v, false
					}
					d.field(v, sf, kv.Value, built, path)
				}
				return v, true
			}
		}
		if len(lit.Elts) != 0 && len(lit.Elts) != t.NumField() {
			return v, false
		}
		for i, elt := range lit.Elts {
			if _, ok := elt.(*ast.KeyValueExpr); ok {
				return v, false
			}
			d.field(v, t.Field(i), elt, built, path)
		}
		return v, true
	case reflect.Slice, reflect.Array:
		if t.Kind() == reflect.Array && len(lit.Elts) > t.Len() {
			return v, false
		}
		if t.Kind() == reflect.Slice {
			v.Set(reflect.MakeSlice(t, len(lit.Elts), len(lit.Elts)))
		}
		for i, elt := range lit.Elts {
			if _, ok := elt.(*ast.KeyValueExpr); ok {
				return v, false
			}
			var b reflect.Value
			if built.IsValid() && i < built.Len() {
				b = built.Index(i)
			}
			v.Index(i).Set(d.decode(elt, t.Elem(), b, index(path, strconv.Itoa(i))))
		}
		return v, true
	case reflect.Map:
		v.Set(reflect.MakeMapWithSize(t, len(lit.Elts)))
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				return v, false
			}
			key, ks, ok := d.key(kv.Key, t.Key())
			if !ok {
				return v, false
			}
			var b reflect.Value
			if built.IsValid() && !built.IsNil() {
				b = built.MapIndex(key)
			}
			at := index(path, ks)
			v.SetMapIndex(key, d.decode(kv.Value, t.Elem(), b, at))
		}
		return v, true
	}
	return v, false
}

// field reads a struct field, unexported or not.
func (d *decoder) field(v reflect.Value, sf reflect.StructField, e ast.Expr, built reflect.Value, path string) {
	var b reflect.Value
	if built.IsValid() {
		b = built.Field(sf.Index[0])
		if !sf.IsExported() {
			b = readable(built, sf)
		}
	}
	fv := v.Field(sf.Index[0])
	if !sf.IsExported() {
		fv = reflect.NewAt(fv.Type(), unsafe.Pointer(fv.UnsafeAddr())).Elem()
	}
	fv.Set(d.decode(e, sf.Type, b, join(path, sf.Name)))
}

// readable is an unexported field of a struct, readable as an exported one is.
func readable(s reflect.Value, sf reflect.StructField) reflect.Value {
	if !s.CanAddr() {
		c := reflect.New(s.Type()).Elem()
		c.Set(s)
		s = c
	}
	f := s.Field(sf.Index[0])
	return reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem()
}

// key reads a map key, which can't be opaque: a basic literal or a constant. Its path is
// spelled as KeyPath spells it.
func (d *decoder) key(e ast.Expr, t reflect.Type) (reflect.Value, string, bool) {
	if b, ok := e.(*ast.BasicLit); ok {
		v := reflect.New(t).Elem()
		c, ok := number(b)
		if !ok || !setConstant(v, c) {
			return v, "", false
		}
		return v, KeyPath(v, b.Kind == token.CHAR), true
	}
	if pkg, name, ok := qualified(e); ok {
		if v, ok := d.constant(pkg, name, t); ok {
			return v, KeyPath(v, false), true
		}
	}
	return reflect.Value{}, "", false
}

// KeyPath is how a path spells a map key: a string quoted, a rune written as a character
// quoted, and any other number in decimal, whatever its type's String method says.
func KeyPath(v reflect.Value, char bool) string {
	switch {
	case v.Kind() == reflect.String:
		return strconv.Quote(v.String())
	case char && v.CanInt():
		return strconv.QuoteRune(rune(v.Int()))
	case v.CanInt():
		return strconv.FormatInt(v.Int(), 10)
	case v.CanUint():
		return strconv.FormatUint(v.Uint(), 10)
	}
	return fmt.Sprint(v.Interface())
}
