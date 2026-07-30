package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func main() {
	var cacheOverride string
	flag.StringVar(&cacheOverride, "gomodcache", "", "Go module cache path (defaults to `go env GOMODCACHE`)")
	flag.Parse()

	cache, err := moduleCache(cacheOverride)
	must(err)

	graph, err := readModuleGraph(filepath.Join("raw", "module-list.txt"))
	must(err)

	f, err := os.Open(filepath.Join("raw", "round3-license-recheck.tsv"))
	must(err)
	defer f.Close()

	reader := csv.NewReader(f)
	reader.Comma = '\t'
	reader.FieldsPerRecord = -1

	rows, err := reader.ReadAll()
	must(err)
	if len(rows) < 2 {
		fatalf("license TSV has no data rows")
	}

	index := headerIndex(rows[0])
	required := []string{
		"module", "version", "evidence_path", "expected_sha256",
		"license_type", "detection_basis", "exists", "hash_match",
		"graph_match", "status",
	}
	for _, name := range required {
		if _, ok := index[name]; !ok {
			fatalf("missing TSV column %q", name)
		}
	}

	verified := 0
	for rowNumber, row := range rows[1:] {
		if len(row) != len(rows[0]) {
			fatalf("row %d has %d columns; want %d", rowNumber+2, len(row), len(rows[0]))
		}

		module := row[index["module"]]
		version := row[index["version"]]
		if _, ok := graph[module+" "+version]; !ok {
			fatalf("%s %s is absent from module-list.txt", module, version)
		}
		if row[index["exists"]] != "true" ||
			row[index["hash_match"]] != "true" ||
			row[index["graph_match"]] != "true" ||
			row[index["status"]] != "OK" {
			fatalf("%s %s has a non-PASS TSV status", module, version)
		}
		if strings.TrimSpace(row[index["detection_basis"]]) == "" {
			fatalf("%s %s has an empty detection basis", module, version)
		}

		licenseType := strings.ToUpper(row[index["license_type"]])
		for _, forbidden := range []string{"GPL", "AGPL", "SSPL", "UNKNOWN", "NOT_FOUND"} {
			if strings.Contains(licenseType, forbidden) {
				fatalf("%s %s has forbidden/unknown license type %q", module, version, licenseType)
			}
		}

		evidencePath := row[index["evidence_path"]]
		const prefix = "<GOMODCACHE>/"
		if !strings.HasPrefix(evidencePath, prefix) {
			fatalf("%s %s has non-portable evidence path %q", module, version, evidencePath)
		}
		relative := strings.TrimPrefix(evidencePath, prefix)
		relative = strings.ReplaceAll(relative, "//", "/")
		licensePath := filepath.Join(cache, filepath.FromSlash(relative))

		content, err := os.ReadFile(licensePath)
		if err != nil {
			fatalf("%s %s license read failed: %v", module, version, err)
		}
		sum := sha256.Sum256(content)
		actual := hex.EncodeToString(sum[:])
		expected := row[index["expected_sha256"]]
		if actual != expected {
			fatalf("%s %s license SHA256 %s; want %s", module, version, actual, expected)
		}
		verified++
	}

	if verified != 75 {
		fatalf("verified %d licenses; want 75", verified)
	}
	fmt.Printf("PASS: %d/75 licenses verified\n", verified)
}

func moduleCache(override string) (string, error) {
	if override != "" {
		return filepath.Clean(override), nil
	}
	out, err := exec.Command("go", "env", "GOMODCACHE").Output()
	if err != nil {
		return "", fmt.Errorf("go env GOMODCACHE: %w", err)
	}
	cache := strings.TrimSpace(string(out))
	if cache == "" {
		return "", fmt.Errorf("go env GOMODCACHE returned an empty path")
	}
	return filepath.Clean(cache), nil
}

func readModuleGraph(path string) (map[string]struct{}, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	graph := make(map[string]struct{})
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) >= 2 {
			graph[fields[0]+" "+fields[1]] = struct{}{}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return graph, nil
}

func headerIndex(header []string) map[string]int {
	index := make(map[string]int, len(header))
	for i, name := range header {
		index[name] = i
	}
	return index
}

func must(err error) {
	if err != nil {
		fatalf("%v", err)
	}
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "FAIL: "+format+"\n", args...)
	os.Exit(1)
}
