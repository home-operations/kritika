package config

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/go-viper/mapstructure/v2"
	"github.com/knadh/koanf/providers/confmap"
	"github.com/knadh/koanf/providers/structs"
	"github.com/knadh/koanf/v2"
	"go.yaml.in/yaml/v4"
)

// LoadSettings reads defaults and legacy environment settings, then the file.
// The complete document is checked for unknown fields by configfile.Parse.
func LoadSettings(raw []byte) (Settings, error) {
	s, err := env.ParseAs[Settings]()
	if err != nil {
		if pe, ok := errors.AsType[env.ParseError](err); ok {
			f, _ := reflect.TypeFor[Settings]().FieldByName(pe.Name)
			return Settings{}, fmt.Errorf("config: environment variable %s: %w", f.Tag.Get("env"), pe.Err)
		}
		return Settings{}, fmt.Errorf("config: %w", err)
	}
	if err := s.ReadFile(raw); err != nil {
		return Settings{}, err
	}
	return s, s.Validate()
}

// ReadFile reads the process settings from a complete application document.
// A file and an environment variable cannot both supply the same setting.
func (s *Settings) ReadFile(raw []byte) error {
	var fields map[string]any
	if err := yaml.Unmarshal(raw, &fields); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	s.fileEnv = map[string]bool{}
	if err := checkSources(reflect.TypeFor[Settings](), fields, "", s.fileEnv); err != nil {
		return err
	}
	k := koanf.New(".")
	if err := k.Load(structs.Provider(*s, "yaml"), nil); err != nil {
		return fmt.Errorf("config: defaults and environment: %w", err)
	}
	if err := k.Load(confmap.Provider(fields, ""), nil); err != nil {
		return fmt.Errorf("config: file: %w", err)
	}
	if err := k.UnmarshalWithConf("", s, koanf.UnmarshalConf{
		Tag: "yaml",
		DecoderConfig: &mapstructure.DecoderConfig{
			DecodeHook: durationHook,
		},
	}); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	return nil
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

func checkSources(typ reflect.Type, fields map[string]any, prefix string, fromFile map[string]bool) error {
	for f := range typ.Fields() {
		key, _, _ := strings.Cut(f.Tag.Get("yaml"), ",")
		value, set := fields[key]
		if !set {
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
