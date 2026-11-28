package tuning

import (
	"errors"
	"fmt"
	"reflect"
	"time"
)

// crossField holds the invariants between fields, which one field's own
// bounds cannot express. The file that declares an area adds its rules here
// in an init, each rule in the file of the field it bounds; each names the
// fields it compares by fieldLabel.
var crossField []func(Values) error

// Validate makes sure that every field is positive, or not negative for a
// field tagged tuning:"zero", and that the cross-field invariants hold. It
// reports every violation, each named by its YAML path and variable.
func (v Values) Validate() error {
	var errs []error
	walk(reflect.ValueOf(v), "tuning.", func(l leaf) {
		if err := l.check(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %s %w", l.path, l.env, err))
		}
	})
	for _, rule := range crossField {
		if err := rule(v); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

var (
	errNotPositive = errors.New("must be positive")
	errNegative    = errors.New("must not be negative")
	errEmpty       = errors.New("must list at least one value")
)

// check is why l's value is out of bounds, or nil.
func (l leaf) check() error {
	zero := l.field.Tag.Get("tuning") == "zero"
	if l.value.Kind() == reflect.Slice {
		if l.value.Len() == 0 && !zero {
			return errEmpty
		}
		for i := range l.value.Len() {
			if err := checkScalar(l.value.Index(i), zero); err != nil {
				return fmt.Errorf("%w (entry %d)", err, i+1)
			}
		}
		return nil
	}
	return checkScalar(l.value, zero)
}

func checkScalar(v reflect.Value, zero bool) error {
	var positive, negative bool
	switch v.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		positive, negative = v.Int() > 0, v.Int() < 0
	case reflect.Float32, reflect.Float64:
		// NaN is neither, and so refused.
		positive, negative = v.Float() > 0, !(v.Float() >= 0)
	default:
		return fmt.Errorf("has a type, %s, that Validate cannot check", v.Type())
	}
	switch {
	case !zero && !positive:
		return errNotPositive
	case negative:
		return errNegative
	}
	return nil
}

// fieldLabel names a field, by its YAML path under tuning, as Validate's
// errors do: its path and its variable.
func fieldLabel(path string) string {
	var env string
	walk(reflect.ValueOf(Values{}), "tuning.", func(l leaf) {
		if l.path == "tuning."+path {
			env = l.env
		}
	})
	return "tuning." + path + ": " + env
}

var (
	errMustExceed    = errors.New("must exceed")
	errMustNotExceed = errors.New("must not exceed")
	errNoDeadline    = errors.New("leaves no runner deadline")
)

// notAbove is the error of a field a that must not exceed field b, or nil.
func notAbove[T int | ByteSize | time.Duration](a string, x T, b string, y T) error {
	if x <= y {
		return nil
	}
	return fmt.Errorf("%s %w %s", fieldLabel(a), errMustNotExceed, fieldLabel(b))
}

// exceeds is the error of a field a that must exceed field b, or nil.
func exceeds[T int | ByteSize | time.Duration](a string, x T, b string, y T) error {
	if x > y {
		return nil
	}
	return fmt.Errorf("%s %w %s", fieldLabel(a), errMustExceed, fieldLabel(b))
}

// atLeastSecond is the error of a field under a second, or nil.
func atLeastSecond(path string, d time.Duration) error {
	if d >= time.Second {
		return nil
	}
	return fmt.Errorf("%s must be at least 1s", fieldLabel(path))
}

// wholeSeconds is the error of a field that is not a whole number of
// seconds, at least one, or nil.
func wholeSeconds(path string, d time.Duration) error {
	if d >= time.Second && d%time.Second == 0 {
		return nil
	}
	return fmt.Errorf("%s must be a whole number of seconds, at least 1s", fieldLabel(path))
}
