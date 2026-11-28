package tuning

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/caarlos0/env/v11"
)

// areaPrefix is the variable prefix of the areas that UPPER_SNAKE would
// split inside a product's name.
var areaPrefix = map[string]string{"gitHub": "GITHUB", "gitLab": "GITLAB", "chatGpt": "CHATGPT"}

func upperSnake(s string) string {
	var b strings.Builder
	for i, r := range s {
		if unicode.IsUpper(r) && i > 0 {
			b.WriteByte('_')
		}
		b.WriteRune(unicode.ToUpper(r))
	}
	return b.String()
}

// TestFieldTags holds every field to the package's convention.
func TestFieldTags(t *testing.T) {
	walk(reflect.ValueOf(Values{}), "", func(l leaf) {
		area, name, ok := strings.Cut(l.path, ".")
		if !ok || strings.Contains(name, ".") {
			t.Errorf("tuning.%s: a field belongs to an area, one level down", l.path)
			return
		}
		prefix, ok := areaPrefix[area]
		if !ok {
			prefix = upperSnake(area)
		}
		if want := "KRITIKA_TUNING_" + prefix + "_" + upperSnake(name); l.field.Tag.Get("env") != want {
			t.Errorf("tuning.%s: env = %q, want %q", l.path, l.field.Tag.Get("env"), want)
		}
		if _, ok := l.field.Tag.Lookup("envDefault"); !ok {
			t.Errorf("tuning.%s: no envDefault", l.path)
		}
		if l.field.Tag.Get("yaml") != name+",omitempty" || name == "" || unicode.IsUpper(rune(name[0])) {
			t.Errorf("tuning.%s: yaml = %q, want a lowerCamel key with omitempty", l.path, l.field.Tag.Get("yaml"))
		}
		if z, ok := l.field.Tag.Lookup("tuning"); ok && z != "zero" {
			t.Errorf("tuning.%s: tuning tag %q, want zero or none", l.path, z)
		}
	})
	for f := range reflect.TypeFor[Values]().Fields() {
		if f.Type.Kind() != reflect.Struct || f.Name != f.Type.Name() {
			t.Errorf("Values.%s: an area is a struct field named for its type", f.Name)
		}
	}
}

// TestDefaultsIgnoreTheEnvironment: Defaults is the tags', not the process's.
func TestDefaultsIgnoreTheEnvironment(t *testing.T) {
	t.Setenv("KRITIKA_TUNING_GATEWAY_SIMILAR_PER_QUERY", "9")
	if got := Defaults().Gateway.SimilarPerQuery; got != 4 {
		t.Fatalf("SimilarPerQuery = %d, want the default 4", got)
	}
	if err := Defaults().Validate(); err != nil {
		t.Fatalf("the defaults are invalid: %v", err)
	}
}

func TestEnvAtDefaults(t *testing.T) {
	if got := Env(Defaults()); len(got) != 0 {
		t.Fatalf("Env(Defaults()) = %v, want none", got)
	}
}

// mutate changes every field of v, which starts at the defaults, to another
// valid value: the first of a few changes that the field's own bounds and
// the invariants take, a fractional one first, so that formatting it is
// tested too.
func mutate(t *testing.T, v *Values) {
	t.Helper()
	var leaves []leaf
	walk(reflect.ValueOf(v).Elem(), "", func(l leaf) { leaves = append(leaves, l) })
	for i, l := range leaves {
		var ok bool
		for _, change := range changes(l.value) {
			try := Defaults()
			change(nth(&try, i))
			if try.Validate() == nil {
				change(l.value)
				ok = true
				break
			}
		}
		if !ok {
			t.Fatalf("tuning.%s: no change of its default is valid", l.path)
		}
	}
}

// nth is v's i-th tunable field, in declaration order.
func nth(v *Values, i int) reflect.Value {
	var out reflect.Value
	var j int
	walk(reflect.ValueOf(v).Elem(), "", func(l leaf) {
		if j == i {
			out = l.value
		}
		j++
	})
	return out
}

// changes are the changes that mutate tries on a field's value, in order.
func changes(v reflect.Value) []func(reflect.Value) {
	add := func(d time.Duration) func(reflect.Value) {
		return func(f reflect.Value) { f.SetInt(f.Int() + int64(d)) }
	}
	switch {
	case v.Kind() == reflect.Slice:
		appendOne := func(d time.Duration) func(reflect.Value) {
			return func(f reflect.Value) { f.Set(reflect.Append(f, reflect.ValueOf(d))) }
		}
		return []func(reflect.Value){appendOne(1500 * time.Millisecond), appendOne(2 * 24 * time.Hour)}
	case v.Kind() == reflect.Float32 || v.Kind() == reflect.Float64:
		return []func(reflect.Value){func(f reflect.Value) { f.SetFloat(f.Float()/2 + 0.125) }}
	case v.Type() == reflect.TypeFor[time.Duration]():
		return []func(reflect.Value){add(1500 * time.Millisecond), add(time.Second), add(-time.Second)}
	default:
		return []func(reflect.Value){add(1), add(-1)}
	}
}

// TestEnvRoundTrip: the variables Env lists parse back into the values
// they came from, for every field.
func TestEnvRoundTrip(t *testing.T) {
	v := Defaults()
	mutate(t, &v)
	pairs := Env(v)
	environment := map[string]string{}
	for _, p := range pairs {
		environment[p.Name] = p.Value
	}
	var fields int
	walk(reflect.ValueOf(v), "", func(leaf) { fields++ })
	if len(environment) != fields || len(pairs) != fields {
		t.Fatalf("Env lists %d variables, want one for each of %d changed fields: %v", len(pairs), fields, pairs)
	}
	got, err := env.ParseAsWithOptions[Values](env.Options{Environment: environment})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, v) {
		t.Fatalf("round trip = %+v, want %+v", got, v)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("mutated values invalid: %v", err)
	}
}

func TestEnvFormats(t *testing.T) {
	v := Defaults()
	v.Gateway.SimilarPerQuery = 6
	if got, want := Env(v), []EnvPair{{"KRITIKA_TUNING_GATEWAY_SIMILAR_PER_QUERY", "6"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Env = %v, want %v", got, want)
	}
}

// TestValidateRefusesZero: every field refuses 0 and -1, by its path and
// variable, unless it is tagged to allow 0.
func TestValidateRefusesZero(t *testing.T) {
	var leaves []leaf
	walk(reflect.ValueOf(Values{}), "tuning.", func(l leaf) { leaves = append(leaves, l) })
	for i, l := range leaves {
		for _, n := range []int64{0, -1} {
			v := Defaults()
			f := nth(&v, i)
			switch f.Kind() {
			case reflect.Slice:
				if n == 0 {
					f.Set(reflect.Zero(f.Type()))
				} else {
					f.Set(reflect.ValueOf([]time.Duration{-1}).Convert(f.Type()))
				}
			case reflect.Float32, reflect.Float64:
				f.SetFloat(float64(n))
			default:
				f.SetInt(n)
			}
			err := v.Validate()
			if n == 0 && l.field.Tag.Get("tuning") == "zero" {
				if err != nil {
					t.Errorf("%s = 0: %v, want it allowed", l.path, err)
				}
				continue
			}
			if want := l.path + ": " + l.env + " must"; err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("%s = %d: Validate = %v, want an error containing %q", l.path, n, err, want)
			}
		}
	}
}

// TestValidateReportsEveryField: one error per bad field, not the first.
func TestValidateReportsEveryField(t *testing.T) {
	v := Defaults()
	v.Gateway.SimilarPerQuery, v.Gateway.SimilarMax = 0, -3
	err := v.Validate()
	for _, want := range []string{
		"tuning.gateway.similarPerQuery: KRITIKA_TUNING_GATEWAY_SIMILAR_PER_QUERY must be positive",
		"tuning.gateway.similarMax: KRITIKA_TUNING_GATEWAY_SIMILAR_MAX must be positive",
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Validate = %v, want %q", err, want)
		}
	}
}

func TestByteSize(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want ByteSize
		text string
		ok   bool
	}{
		{"0", 0, "0", true},
		{"1", 1, "1", true},
		{"1024", 1 << 10, "1Ki", true},
		{"1536", 1536, "1536", true},
		{"1Ki", 1 << 10, "1Ki", true},
		{"1536Ki", 1536 << 10, "1536Ki", true},
		{"2Mi", 2 << 20, "2Mi", true},
		{"1024Mi", 1 << 30, "1Gi", true},
		{"2Gi", 2 << 30, "2Gi", true},
		{"3Ti", 3 << 40, "3Ti", true},
		{"8388607Ti", 8388607 << 40, "8388607Ti", true},
		{"8388608Ti", 0, "", false},
		{"-1", 0, "", false},
		{"1.5Gi", 0, "", false},
		{"1G", 0, "", false},
		{"", 0, "", false},
	} {
		t.Run(tt.in, func(t *testing.T) {
			var got ByteSize
			err := got.UnmarshalText([]byte(tt.in))
			if (err == nil) != tt.ok || (tt.ok && got != tt.want) {
				t.Fatalf("UnmarshalText(%q) = %d, %v; want %d, ok %v", tt.in, got, err, tt.want, tt.ok)
			}
			if !tt.ok {
				return
			}
			text, _ := got.MarshalText()
			if string(text) != tt.text || got.String() != tt.text {
				t.Fatalf("MarshalText = %q, String = %q, want %q", text, got.String(), tt.text)
			}
			var back ByteSize
			if err := back.UnmarshalText(text); err != nil || back != got {
				t.Fatalf("round trip of %q = %d, %v; want %d", text, back, err, got)
			}
		})
	}
}

func TestOverrideRestores(t *testing.T) {
	before := Get()
	t.Run("override", func(t *testing.T) {
		Override(t, func(v *Values) { v.Gateway.SimilarMax = 3 })
		if got := Get().Gateway.SimilarMax; got != 3 {
			t.Fatalf("SimilarMax = %d, want the override 3", got)
		}
		if before.Gateway.SimilarMax != 10 {
			t.Fatal("Override changed the values it replaced")
		}
	})
	if Get() != before {
		t.Fatalf("values after the test = %+v, want those before it", *Get())
	}
}

func TestSetCopies(t *testing.T) {
	before := Get()
	t.Cleanup(func() { current.Store(before) })
	v := Defaults()
	Set(v)
	// Values is passed by value, so only a slice can share its argument's
	// memory.
	want := v.Agent.StepRetryWaits[0]
	v.Agent.StepRetryWaits[0] = time.Hour
	if got := Get().Agent.StepRetryWaits[0]; got != want {
		t.Fatalf("StepRetryWaits[0] = %s after the argument changed, want %s: Set kept its argument's slice", got, want)
	}
}

// TestEnvEmptyList: an empty list is spelled EmptyList, because env reads an
// empty variable as unset, and so as the default, and it reads back empty.
func TestEnvEmptyList(t *testing.T) {
	v := Defaults()
	v.WebAPI.TokenLifetimes = Durations{}
	pairs := Env(v)
	if want := []EnvPair{{"KRITIKA_TUNING_WEB_API_TOKEN_LIFETIMES", EmptyList}}; !reflect.DeepEqual(pairs, want) {
		t.Fatalf("Env = %v, want %v", pairs, want)
	}
	got, err := env.ParseAsWithOptions[Values](env.Options{Environment: map[string]string{pairs[0].Name: pairs[0].Value}})
	if err != nil {
		t.Fatal(err)
	}
	if got.WebAPI.TokenLifetimes == nil || len(got.WebAPI.TokenLifetimes) != 0 {
		t.Fatalf("TokenLifetimes = %#v, want an empty list", got.WebAPI.TokenLifetimes)
	}
	if err := got.Validate(); err != nil {
		t.Fatalf("an empty TokenLifetimes is invalid: %v", err)
	}
	if _, err := env.ParseAsWithOptions[Values](env.Options{Environment: map[string]string{pairs[0].Name: "7d"}}); err == nil {
		t.Fatal("a list that does not parse was read")
	}
}

// TestValidateRefusesPastTheCeiling: every integer field refuses one past
// its type's ceiling, by its path and variable.
func TestValidateRefusesPastTheCeiling(t *testing.T) {
	var leaves []leaf
	walk(reflect.ValueOf(Values{}), "tuning.", func(l leaf) { leaves = append(leaves, l) })
	for i, l := range leaves {
		if l.value.Kind() == reflect.Float64 {
			continue
		}
		v := Defaults()
		j := 0
		walk(reflect.ValueOf(&v).Elem(), "", func(f leaf) {
			switch {
			case j != i:
			case f.value.Kind() == reflect.Slice:
				most, _ := ceiling(f.value.Index(0))
				f.value.Set(reflect.ValueOf(Durations{time.Duration(most + 1)}).Convert(f.value.Type()))
			default:
				most, _ := ceiling(f.value)
				f.value.SetInt(most + 1)
			}
			j++
		})
		want := l.path + ": " + l.env + " must be at most"
		if err := v.Validate(); !strings.Contains(fmt.Sprint(err), want) {
			t.Errorf("%s past its ceiling: Validate = %v, want %q", l.path, err, want)
		}
	}
	if _, text := ceiling(reflect.ValueOf(time.Hour)); text != "87600h0m0s" {
		t.Errorf("duration ceiling = %s", text)
	}
}
