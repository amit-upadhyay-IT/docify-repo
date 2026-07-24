package architecture

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

type packageInfo struct {
	ImportPath string
	Imports    []string
}

func TestCurrentImportsFollowArchitecture(t *testing.T) {
	root := moduleRoot(t)
	module := modulePath(t, root)
	packages := modulePackages(t, root)

	if violations := architectureViolations(module, packages); len(violations) != 0 {
		t.Fatalf("architecture violations:\n%s", strings.Join(violations, "\n"))
	}
}

func TestArchitecturePolicyRejectsForbiddenImports(t *testing.T) {
	const module = "example.test/docify-repo"
	tests := []struct {
		name     string
		packages []packageInfo
	}{
		{
			name: "command imports handler",
			packages: []packageInfo{{
				ImportPath: module + "/cmd/docify-repo",
				Imports:    []string{module + "/internal/app/documentation/handler"},
			}},
		},
		{
			name: "handler imports repository",
			packages: []packageInfo{{
				ImportPath: module + "/internal/app/documentation/handler",
				Imports:    []string{module + "/internal/repository/git"},
			}},
		},
		{
			name: "usecase imports HTTP",
			packages: []packageInfo{{
				ImportPath: module + "/internal/app/documentation/usecase",
				Imports:    []string{"net/http"},
			}},
		},
		{
			name: "repository imports application",
			packages: []packageInfo{{
				ImportPath: module + "/internal/repository/filesystem",
				Imports:    []string{module + "/internal/app/documentation/model"},
			}},
		},
		{
			name: "repositories import each other",
			packages: []packageInfo{{
				ImportPath: module + "/internal/repository/git",
				Imports:    []string{module + "/internal/repository/filesystem"},
			}},
		},
		{
			name: "application components import each other",
			packages: []packageInfo{{
				ImportPath: module + "/internal/app/documentation/usecase",
				Imports:    []string{module + "/internal/app/publishing/model"},
			}},
		},
		{
			name: "interfaces package exists",
			packages: []packageInfo{{
				ImportPath: module + "/internal/interfaces",
			}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if violations := architectureViolations(module, test.packages); len(violations) == 0 {
				t.Fatal("architectureViolations() returned no violation")
			}
		})
	}
}

func architectureViolations(module string, packages []packageInfo) []string {
	var violations []string
	for _, pkg := range packages {
		if hasPathSegment(pkg.ImportPath, "interfaces") {
			violations = append(violations, fmt.Sprintf("forbidden interfaces package: %s", pkg.ImportPath))
		}

		for _, imported := range pkg.Imports {
			if violation := importViolation(module, pkg.ImportPath, imported); violation != "" {
				violations = append(violations, violation)
			}
		}
	}
	sort.Strings(violations)
	return violations
}

func importViolation(module, importing, imported string) string {
	command := module + "/cmd/docify-repo"
	app := module + "/internal/app/"
	documentation := app + "documentation/"
	transport := documentation + "transport"
	handler := documentation + "handler"
	usecase := documentation + "usecase"
	repositories := module + "/internal/repository/"

	if within(importing, command) && localImport(module, imported) {
		if imported != module+"/internal/config" && imported != transport {
			return forbiddenImport(importing, imported)
		}
	}

	if within(importing, transport) && localImport(module, imported) {
		allowed := []string{
			module + "/internal/config",
			handler,
			usecase,
			documentation + "model",
			module + "/internal/model",
			module + "/internal/repository",
		}
		if !withinAny(imported, allowed) {
			return forbiddenImport(importing, imported)
		}
	}

	if within(importing, handler) && localImport(module, imported) {
		if !withinAny(imported, []string{documentation + "model", usecase}) {
			return forbiddenImport(importing, imported)
		}
	}

	if within(importing, usecase) {
		for _, forbidden := range []string{"net/http", "database/sql", "os/exec"} {
			if imported == forbidden || strings.HasPrefix(imported, forbidden+"/") {
				return forbiddenImport(importing, imported)
			}
		}
		if localImport(module, imported) && !withinAny(imported, []string{
			documentation + "model",
			module + "/internal/model",
			module + "/internal/prompt",
		}) {
			return forbiddenImport(importing, imported)
		}
	}

	if strings.HasPrefix(importing, repositories) && localImport(module, imported) {
		if strings.HasPrefix(imported, app) {
			return forbiddenImport(importing, imported)
		}
		if strings.HasPrefix(imported, repositories) {
			if repositoryName(importing, repositories) != repositoryName(imported, repositories) {
				return forbiddenImport(importing, imported)
			}
		} else if !within(imported, module+"/internal/model") {
			return forbiddenImport(importing, imported)
		}
	}

	if strings.HasPrefix(importing, app) && strings.HasPrefix(imported, app) {
		if applicationName(importing, app) != applicationName(imported, app) {
			return forbiddenImport(importing, imported)
		}
	}

	return ""
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	directory, err := filepath.Abs(".")
	if err != nil {
		t.Fatalf("resolve test directory: %v", err)
	}

	for {
		command := exec.Command("go", "env", "GOMOD")
		command.Dir = directory
		output, err := command.Output()
		if err == nil && strings.TrimSpace(string(output)) == filepath.Join(directory, "go.mod") {
			return directory
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			t.Fatal("go.mod not found")
		}
		directory = parent
	}
}

func modulePath(t *testing.T, root string) string {
	t.Helper()
	command := exec.Command("go", "list", "-m", "-f", "{{.Path}}")
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("go list module: %v: %s", err, output)
	}
	return strings.TrimSpace(string(output))
}

func modulePackages(t *testing.T, root string) []packageInfo {
	t.Helper()
	command := exec.Command("go", "list", "-json", "./...")
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("go list packages: %v: %s", err, output)
	}

	decoder := json.NewDecoder(bytes.NewReader(output))
	var packages []packageInfo
	for {
		var pkg packageInfo
		if err := decoder.Decode(&pkg); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("decode go list output: %v", err)
		}
		packages = append(packages, pkg)
	}
	return packages
}

func forbiddenImport(importing, imported string) string {
	return fmt.Sprintf("%s must not import %s", importing, imported)
}

func localImport(module, imported string) bool {
	return imported == module || strings.HasPrefix(imported, module+"/")
}

func within(path, prefix string) bool {
	return path == prefix || strings.HasPrefix(path, prefix+"/")
}

func withinAny(path string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if within(path, prefix) {
			return true
		}
	}
	return false
}

func hasPathSegment(path, segment string) bool {
	for _, candidate := range strings.Split(path, "/") {
		if candidate == segment {
			return true
		}
	}
	return false
}

func applicationName(path, prefix string) string {
	return firstPathSegment(strings.TrimPrefix(path, prefix))
}

func repositoryName(path, prefix string) string {
	return firstPathSegment(strings.TrimPrefix(path, prefix))
}

func firstPathSegment(path string) string {
	if index := strings.IndexByte(path, '/'); index >= 0 {
		return path[:index]
	}
	return path
}
