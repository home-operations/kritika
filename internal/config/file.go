package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"os"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/structs"
	"github.com/knadh/koanf/v2"
	"go.yaml.in/yaml/v4"
)

// Document holds the original bytes for hashing and the policy nodes that
// configfile decodes. Operational settings are consumed separately by koanf.
type Document struct {
	Source []byte
	Policy yaml.Node
}

// LoadSettings reads defaults and operational environment settings, then the file.
// The remaining document is checked for unknown fields by configfile.Parse.
func LoadSettings(raw []byte) (Settings, Document, error) {
	s, err := env.ParseAs[Settings]()
	if err != nil {
		return Settings{}, Document{}, envError[Settings](err)
	}
	doc, err := s.readFile(raw)
	if err != nil {
		return Settings{}, Document{}, err
	}
	return s, doc, s.Validate()
}

// envError names the environment variable a value of T failed to parse from:
// env reports the Go field.
func envError[T any](err error) error {
	if pe, ok := errors.AsType[env.ParseError](err); ok {
		f, _ := reflect.TypeFor[T]().FieldByName(pe.Name)
		return fmt.Errorf("config: environment variable %s: %w", f.Tag.Get("env"), pe.Err)
	}
	return fmt.Errorf("config: %w", err)
}

// readFile reads the process settings from a complete application document.
// A file and an environment variable cannot both supply the same setting.
func (s *Settings) readFile(raw []byte) (Document, error) {
	runtime, doc, err := splitDocument(raw)
	if err != nil {
		return Document{}, err
	}
	fromFile := map[string]bool{}
	if err := checkSources(reflect.TypeFor[Settings](), runtime, "", fromFile); err != nil {
		return Document{}, err
	}
	k := koanf.New(".")
	if err := k.Load(structs.Provider(*s, "yaml"), nil); err != nil {
		return Document{}, fmt.Errorf("config: defaults and environment: %w", err)
	}
	if err := k.Load(confmap.Provider(runtime, ""), nil); err != nil {
		return Document{}, fmt.Errorf("config: file: %w", err)
	}
	var merged Settings
	if err := k.UnmarshalWithConf("", &merged, koanf.UnmarshalConf{
		Tag: "yaml",
		DecoderConfig: &mapstructure.DecoderConfig{
			DecodeHook:  mapstructure.ComposeDecodeHookFunc(durationHook, byteSizeHook, integerHook),
			ErrorUnused: true,
			MatchName:   func(key, field string) bool { return key == field },
		},
	}); err != nil {
		return Document{}, fmt.Errorf("config: %w", err)
	}
	merged.fileEnv = fromFile
	*s = merged
	return doc, nil
}

func splitDocument(raw []byte) (map[string]any, Document, error) {
	var fields map[string]yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&fields); err != nil && !errors.Is(err, io.EOF) {
		return nil, Document{}, fmt.Errorf("config: parse: %w", err)
	}
	var extra yaml.Node
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, Document{}, errors.New("config: parse: the file must hold one document")
	}
	runtime := map[string]any{}
	for f := range reflect.TypeFor[Settings]().Fields() {
		key, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		if key == "" {
			continue
		}
		if node, ok := fields[key]; ok {
			var value any
			if err := node.Decode(&value); err != nil {
				return nil, Document{}, fmt.Errorf("config: %s: %w", key, err)
			}
			runtime[key] = value
			delete(fields, key)
		}
	}
	doc := Document{Source: raw, Policy: yaml.Node{Kind: yaml.MappingNode}}
	for _, key := range slices.Sorted(maps.Keys(fields)) {
		node := fields[key]
		doc.Policy.Content = append(doc.Policy.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key, Line: node.Line}, &node)
	}
	return runtime, doc, nil
}

func durationHook(from, to reflect.Type, value any) (any, error) {
	if to != reflect.TypeFor[time.Duration]() || from == to {
		return value, nil
	}
	if from.Kind() != reflect.String {
		return nil, errors.New("duration must be a string with units")
	}
	return time.ParseDuration(value.(string))
}

func byteSizeHook(from, to reflect.Type, value any) (any, error) {
	if to != reflect.TypeFor[ByteSize]() || from == to {
		return value, nil
	}
	switch value.(type) {
	case string, int, int64, uint64:
	default:
		return nil, errors.New("size must be a whole number of bytes or a string with units")
	}
	var b ByteSize
	err := b.UnmarshalText([]byte(fmt.Sprint(value)))
	return b, err
}

// mapstructure truncates floating-point values even with weak typing disabled.
func integerHook(_, to reflect.Type, value any) (any, error) {
	if to.Kind() != reflect.Int {
		return value, nil
	}
	switch n := value.(type) {
	case float64:
		if math.IsNaN(n) || math.Trunc(n) != n || n < float64(math.MinInt) || n >= -float64(math.MinInt) {
			return nil, errors.New("value must be a whole number within the integer range")
		}
	case uint64:
		if n > uint64(math.MaxInt) {
			return nil, errors.New("value exceeds the integer range")
		}
	}
	return value, nil
}

func checkSources(typ reflect.Type, fields map[string]any, prefix string, fromFile map[string]bool) error {
	for f := range typ.Fields() {
		key, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		value, set := fields[key]
		if !set {
			continue
		}
		if value == nil {
			delete(fields, key)
			continue
		}
		path := prefix + key
		if f.Anonymous {
			if nested, ok := value.(map[string]any); ok {
				if err := checkSources(f.Type, nested, path+".", fromFile); err != nil {
					return err
				}
			}
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("env"), ",")
		if _, exists := os.LookupEnv(name); exists {
			return fmt.Errorf("config: %s conflicts with %s: remove the environment variable and keep the file setting", path, name)
		}
		fromFile[name] = true
	}
	return nil
}
