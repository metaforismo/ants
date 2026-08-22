// Package fixtures holds the embedded demo application used by the tranche 1
// vertical slice. The pipeline executes real commands against real commits in
// this repository; nothing about the flow itself is faked.
package fixtures

import (
	"embed"
	"fmt"
	"io/fs"
	"path"
	"path/filepath"
	"strings"

	"github.com/metaforismo/ants/internal/planner"
	"github.com/metaforismo/ants/internal/sandbox"
	"github.com/metaforismo/ants/internal/scm"
)

//go:embed all:demoapp
var demoFS embed.FS

// DemoName identifies the fixture registered with the seeder.
const DemoName = "calc-demo"

// ScriptFake registers deterministic passing results for every command the
// fixture declares. The fake driver executes nothing; scripting exactly the
// declared commands keeps its behavior explicit rather than surprising.
func ScriptFake(fd *sandbox.FakeDriver) error {
	catalog, err := planner.ParseCatalog(DemoSeed().Files)
	if err != nil {
		return err
	}
	for i := range catalog.Capabilities {
		capability := &catalog.Capabilities[i]
		for _, task := range capability.Tasks {
			for _, cmd := range task.VerifyCommands {
				fd.Script(strings.Join(cmd, " "), sandbox.ExecResult{ExitCode: 0})
			}
		}
		for _, cmd := range capability.VerifyAll {
			fd.Script(strings.Join(cmd, " "), sandbox.ExecResult{
				ExitCode: 0,
				Stdout:   []byte("scripted-ok\n"),
			})
		}
	}
	return nil
}

// DemoSeed returns the initial repository content for the demo project.
func DemoSeed() scm.Seed {
	files := map[string][]byte{}
	err := fs.WalkDir(demoFS, "demoapp", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, trimErr := filepath.Rel("demoapp", p)
		if trimErr != nil {
			return trimErr
		}
		content, readErr := demoFS.ReadFile(p)
		if readErr != nil {
			return readErr
		}
		files[rel] = content
		return nil
	})
	if err != nil {
		panic(fmt.Sprintf("embedded demo fixture unreadable: %v", err))
	}
	return scm.Seed{DefaultBranch: "main", Files: files}
}

// DemoFile returns a single embedded file; used by tests that inspect the
// capability catalog without initializing a repo.
func DemoFile(rel string) []byte {
	b, _ := demoFS.ReadFile(path.Join("demoapp", rel))
	return b
}
