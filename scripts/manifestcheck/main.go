// Command manifestcheck verifies supply-chain discipline: every direct Go
// dependency in go.mod must have a recorded entry in third_party/manifest.yaml
// with license and adoption decision. Undocumented dependencies fail the build.
package main

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type manifestEntry struct {
	Module   string `yaml:"module"`
	Version  string `yaml:"version"`
	License  string `yaml:"license"`
	Decision string `yaml:"decision"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "manifest-check: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("manifest-check: all direct dependencies are documented")
}

func run() error {
	direct, err := directRequires("go.mod")
	if err != nil {
		return err
	}
	raw, err := os.ReadFile("third_party/manifest.yaml")
	if err != nil {
		return fmt.Errorf("read third_party/manifest.yaml: %w", err)
	}
	var doc struct {
		Dependencies []manifestEntry `yaml:"dependencies"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("parse third_party/manifest.yaml: %w", err)
	}
	documented := map[string]manifestEntry{}
	for _, entry := range doc.Dependencies {
		documented[entry.Module] = entry
	}

	failed := false
	for _, module := range direct {
		entry, ok := documented[module]
		if !ok {
			fmt.Fprintf(os.Stderr, "MISSING  %-45s not recorded in third_party/manifest.yaml\n", module)
			failed = true
			continue
		}
		switch {
		case entry.License == "":
			fmt.Fprintf(os.Stderr, "NO LICENSE FIELD for %s\n", module)
			failed = true
		case entry.Decision == "":
			fmt.Fprintf(os.Stderr, "NO DECISION FIELD for %s\n", module)
			failed = true
		default:
			fmt.Printf("OK       %-45s (%s, %s)\n", module, entry.License, entry.Decision)
		}
	}
	if failed {
		return fmt.Errorf("undocumented dependencies found; update third_party/manifest.yaml")
	}
	return nil
}

// directRequires extracts non-indirect modules from the top-level require
// block of go.mod. Hand-parsed deliberately: the file format is stable and
// pulling a full module parser here would be the only consumer.
func directRequires(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []string
	inBlock := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "require ("):
			inBlock = true
		case inBlock && line == ")":
			inBlock = false
		case inBlock:
			if strings.Contains(line, "// indirect") || line == "" || strings.HasPrefix(line, "//") {
				continue
			}
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				out = append(out, fields[0])
			}
		case strings.HasPrefix(line, "require "):
			fields := strings.Fields(strings.TrimPrefix(line, "require "))
			if len(fields) >= 2 && !strings.Contains(line, "// indirect") {
				out = append(out, fields[0])
			}
		}
	}
	return out, nil
}
