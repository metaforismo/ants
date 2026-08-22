package config

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Load builds the effective configuration: defaults, then the YAML file at
// path (optional), then ANTS_* environment overrides, then validation. A
// missing file when path is empty is normal; a missing file at an explicit
// path is an error.
func Load(path string) (Config, error) {
	return load(path, os.LookupEnv)
}

func load(path string, lookup LookupFunc) (Config, error) {
	cfg := Defaults()
	if path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return Config{}, fmt.Errorf("read config %s: %w", path, err)
		}
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)
		if err := dec.Decode(&cfg); err != nil {
			return Config{}, fmt.Errorf("parse config %s: %w", path, err)
		}
	}
	if err := cfg.ApplyEnv(lookup); err != nil {
		return Config{}, err
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
