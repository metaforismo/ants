// Package db embeds the canonical PostgreSQL migrations so any Go binary can
// apply them without external tooling.
package db

import (
	"embed"
	"io/fs"
	"sort"
	"strings"
)

//go:embed migrations/*.sql
var embedded embed.FS

// Migration is one ordered, forward-only schema change.
type Migration struct {
	Version string
	Name    string
	SQL     string
}

// All returns every migration ordered by version. Names follow
// NNNN_description.sql; ordering is lexical by filename prefix.
func All() ([]Migration, error) {
	entries, err := fs.ReadDir(embedded, "migrations")
	if err != nil {
		return nil, err
	}
	var out []Migration
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		content, err := embedded.ReadFile("migrations/" + entry.Name())
		if err != nil {
			return nil, err
		}
		base := strings.TrimSuffix(entry.Name(), ".sql")
		parts := strings.SplitN(base, "_", 2)
		if len(parts) != 2 || len(parts[0]) != 4 {
			return nil, fs.ErrInvalid
		}
		out = append(out, Migration{
			Version: parts[0],
			Name:    parts[1],
			SQL:     string(content),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Version < out[j].Version })
	return out, nil
}
