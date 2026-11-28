package tuning

import (
	"encoding"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/caarlos0/env/v11"
)

// current is the process's values. It holds the defaults until a main
// calls Set, so tests and library callers need no setup.
var current atomic.Pointer[Values]

func init() {
	d := Defaults()
	current.Store(&d)
}

// Defaults is Values as the envDefault tags spell it, whatever the
// environment holds.
func Defaults() Values {
	v, err := env.ParseAsWithOptions[Values](env.Options{Environment: map[string]string{}})
	if err != nil {
		// Only a malformed envDefault tag fails, which the package's tests
		// catch.
		panic(fmt.Sprintf("tuning: defaults: %v", err))
	}
	return v
}

// Get returns the process's values. Callers must treat them as read-only:
// they are shared with every goroutine.
func Get() *Values { return current.Load() }

// Set makes v the process's values. A main calls it once, after the
// configuration is loaded and validated.
func Set(v Values) {
	v = clone(v)
	current.Store(&v)
}

// Override sets the process's values to a copy of the current ones that
// mutate changes, for the rest of the test, and restores the previous ones
// when it ends. The values are process-wide, so a test that calls it must not
// run in parallel with one that reads them.
func Override(t testing.TB, mutate func(*Values)) {
	t.Helper()
	prev := Get()
	next := clone(*prev)
	mutate(&next)
	current.Store(&next)
	t.Cleanup(func() { current.Store(prev) })
}

// EnvPair is one environment variable, by name and value.
type EnvPair struct {
	Name, Value string
}

// Env lists, in declaration order, the variables that make a process's
// defaults into v: one for each field that differs from its default,
// formatted so that the environment parse reads back the same value.
func Env(v Values) []EnvPair {
	var defaults []reflect.Value
	walk(reflect.ValueOf(Defaults()), "", func(l leaf) { defaults = append(defaults, l.value) })
	var out []EnvPair
	var i int
	walk(reflect.ValueOf(v), "", func(l leaf) {
		if !reflect.DeepEqual(l.value.Interface(), defaults[i].Interface()) {
			out = append(out, EnvPair{Name: l.env, Value: format(l)})
		}
		i++
	})
	return out
}

// leaf is one tunable field: its YAML path, its variable, and its value.
type leaf struct {
	path  string
	env   string
	field reflect.StructField
	value reflect.Value
}

// walk calls fn for each tunable field of v, a Values or an area, in
// declaration order, with the field's YAML path after prefix.
func walk(v reflect.Value, prefix string, fn func(leaf)) {
	for i := range v.NumField() {
		f := v.Type().Field(i)
		key, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if f.Type.Kind() == reflect.Struct {
			walk(v.Field(i), prefix+key+".", fn)
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("env"), ",")
		fn(leaf{path: prefix + key, env: name, field: f, value: v.Field(i)})
	}
}

// clone copies v's slices, so that changing one copy leaves the other as
// it was.
func clone(v Values) Values {
	walk(reflect.ValueOf(&v).Elem(), "", func(l leaf) {
		if l.value.Kind() == reflect.Slice && !l.value.IsNil() {
			c := reflect.MakeSlice(l.value.Type(), l.value.Len(), l.value.Len())
			reflect.Copy(c, l.value)
			l.value.Set(c)
		}
	})
	return v
}

// format is a field's value as its environment variable spells it. An
// empty list is EmptyList: env reads an empty variable as unset.
func format(l leaf) string {
	if l.value.Kind() == reflect.Slice {
		if l.value.Len() == 0 {
			return EmptyList
		}
		parts := make([]string, l.value.Len())
		for i := range parts {
			parts[i] = formatScalar(l.path, l.value.Index(i))
		}
		return strings.Join(parts, ",")
	}
	return formatScalar(l.path, l.value)
}

func formatScalar(path string, v reflect.Value) string {
	switch x := v.Interface().(type) {
	case encoding.TextMarshaler:
		b, err := x.MarshalText()
		if err != nil {
			panic(fmt.Sprintf("tuning: %s: %v", path, err))
		}
		return string(b)
	case time.Duration:
		return x.String()
	}
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(v.Int(), 10)
	case reflect.Float32, reflect.Float64:
		return strconv.FormatFloat(v.Float(), 'g', -1, 64)
	default:
		panic(fmt.Sprintf("tuning: %s: %s has no environment form", path, v.Type()))
	}
}
