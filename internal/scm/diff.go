package scm

import (
	"fmt"
	"sort"
	"strings"
)

// renderUnifiedDiff produces a minimal unified diff between two file trees.
// It is deterministic and used for artifacts and review input in memory mode;
// the local_git driver uses real git diffs.
func renderUnifiedDiff(base, head map[string][]byte) []byte {
	var out strings.Builder
	fmt.Fprintf(&out, "diff --summary base..head\n")

	all := map[string]bool{}
	for p := range base {
		all[p] = true
	}
	for p := range head {
		all[p] = true
	}
	paths := make([]string, 0, len(all))
	for p := range all {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	for _, p := range paths {
		b, inBase := base[p]
		h, inHead := head[p]
		switch {
		case !inBase:
			fmt.Fprintf(&out, "added file %s (%d lines)\n", p, countLines(h))
			writeHunks(&out, nil, h)
		case !inHead:
			fmt.Fprintf(&out, "removed file %s\n", p)
		case string(b) != string(h):
			fmt.Fprintf(&out, "modified file %s\n", p)
			writeHunks(&out, b, h)
		default:
			fmt.Fprintf(&out, "unchanged file %s\n", p)
		}
	}
	return []byte(out.String())
}

func writeHunks(out *strings.Builder, base, head []byte) {
	baseLines := splitLines(string(base))
	headLines := splitLines(string(head))
	max := len(baseLines)
	if len(headLines) > max {
		max = len(headLines)
	}
	for i := range max {
		var b, h string
		if i < len(baseLines) {
			b = baseLines[i]
		}
		if i < len(headLines) {
			h = headLines[i]
		}
		switch {
		case b == h:
			fmt.Fprintf(out, "  %s\n", b)
		case b == "":
			fmt.Fprintf(out, "+%s\n", h)
		case h == "":
			fmt.Fprintf(out, "-%s\n", b)
		default:
			fmt.Fprintf(out, "-%s\n+%s\n", b, h)
		}
	}
}

func splitLines(s string) []string {
	s = strings.TrimSuffix(s, "\n")
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

func countLines(b []byte) int {
	return len(splitLines(string(b)))
}
