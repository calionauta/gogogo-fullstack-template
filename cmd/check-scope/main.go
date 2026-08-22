// SCOPE:core - DO NOT REMOVE - SCOPE annotation linter (pre-commit gate).
// Package main implements the check-scope linter referenced by the
// Makefile (`make check-scope`) and the lefthook pre-commit pipeline.
//
// It enforces that every hand-written .go file under internal/ and
// features/ carries a `// SCOPE:` annotation so template consumers can
// identify what is safe to remove. Generated (*_templ.go) and test files
// are exempt.
//
// Accepted annotation dialects (both are in use across this repo):
//
//	// SCOPE:core    [- description]
//	// SCOPE:feature [- description]
//	// SCOPE:layer=<name>[,...][,removal=...]
package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const maxScanLines = 40

var validLayers = map[string]bool{
	"core":    true,
	"feature": true,
}

func main() {
	root := "."
	if len(os.Args) > 1 {
		root = os.Args[1]
	}

	offenders, err := collectOffenders(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "check-scope: %v\n", err)
		os.Exit(1)
	}

	if len(offenders) == 0 {
		fmt.Println("✅ SCOPE annotations present in internal/ + features/")
		return
	}
	report(offenders)
	os.Exit(2)
}

func collectOffenders(root string) ([]string, error) {
	var offenders []string

	// #nosec G703 -- local dev tool; root comes from the developer's own shell.
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return skipRule(path, root, d.Name())
		}
		if !isLintable(path) || !inScopeDir(path) {
			return nil
		}
		ok, err := hasScopeAnnotation(path)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if !ok {
			offenders = append(offenders, path)
		}
		return nil
	})

	return offenders, err
}

func skipRule(path, root, name string) error {
	isRoot := filepath.Clean(path) == filepath.Clean(root)
	if !isRoot && (name == "vendor" || name == "tmp" || name == "node_modules" || strings.HasPrefix(name, ".")) {
		return filepath.SkipDir
	}
	return nil
}

func isLintable(path string) bool {
	base := filepath.Base(path)
	switch {
	case !strings.HasSuffix(path, ".go"):
		return false
	case strings.HasSuffix(base, "_templ.go"), strings.HasSuffix(base, "_test.go"):
		return false
	default:
		return true
	}
}

func inScopeDir(path string) bool {
	p := filepath.ToSlash(path)
	return strings.HasPrefix(p, "internal/") || strings.HasPrefix(p, "features/")
}

func hasScopeAnnotation(path string) (bool, error) {
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for i := 0; sc.Scan() && i < maxScanLines; i++ {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "":
			continue
		case strings.HasPrefix(sc.Text(), "package "):
			return false, nil // package clause reached without annotation
		case scopeFromLine(line):
			return true, nil
		}
	}
	return false, sc.Err()
}

func scopeFromLine(line string) bool {
	rest, ok := strings.CutPrefix(line, "//")
	if !ok {
		return false
	}
	rest = strings.TrimSpace(rest)
	layer, ok := strings.CutPrefix(rest, "SCOPE:")
	if !ok {
		return false
	}
	head, _, _ := strings.Cut(strings.TrimSpace(layer), " ")
	return validLayers[head] || strings.HasPrefix(head, "layer=")
}

func report(offenders []string) {
	fmt.Fprintf(os.Stderr, "❌ %d file(s) missing '// SCOPE:' annotation:\n", len(offenders))
	for _, f := range offenders {
		fmt.Fprintf(os.Stderr, "  %s\n", f)
	}
	fmt.Fprintln(os.Stderr, "\nAdd one of:")
	fmt.Fprintln(os.Stderr, "  // SCOPE:core - why this file must not be removed")
	fmt.Fprintln(os.Stderr, "  // SCOPE:feature - REMOVE by deleting <dir>/")
}
