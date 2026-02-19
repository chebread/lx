package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func runAndCapture(opts options, rootDir string) ([]TraceData, error) {
	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()

	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, err
	}

	entryPoints, err := findMainPackages(absRoot)
	if err != nil {
		return nil, fmt.Errorf("failed to scan for main packages: %w", err)
	}

	if len(entryPoints) == 0 {
		return nil, fmt.Errorf("no executable 'package main' found under %s", rootDir)
	}

	goExe, err := exec.LookPath("go")
	if err != nil {
		return nil, fmt.Errorf("go not found in PATH: %w", err)
	}

	var allTraces []TraceData
	var executionErrors []string

	for _, dir := range entryPoints {

		relDir, _ := filepath.Rel(absRoot, dir)
		if relDir == "" {
			relDir = "."
		}
		fmt.Printf("\t[Exec] Running entry point: %s\n", relDir)

		traces, err := executeSinglePackage(ctx, goExe, dir, opts)
		if err != nil {

			executionErrors = append(executionErrors, fmt.Sprintf("%s: %v", relDir, err))
			continue
		}
		allTraces = append(allTraces, traces...)
	}

	if len(executionErrors) > 0 {
		errMsg := strings.Join(executionErrors, "\n\t- ")

		return allTraces, fmt.Errorf("execution failed in:\n\t- %s", errMsg)
	}

	return allTraces, nil
}

func executeSinglePackage(ctx context.Context, goExe, dir string, opts options) ([]TraceData, error) {
	args := []string{"run"}
	if opts.tags != "" {
		args = append(args, "-tags", opts.tags)
	}
	args = append(args, ".")
	cmd := exec.CommandContext(ctx, goExe, args...)
	cmd.Dir = dir

	secureEnv := buildSecureEnvAllowlist()
	token := mustRandomToken(16)

	cmd.Env = append(secureEnv,
		"LX_MODE=capture",
		"LX_TRACE_TOKEN="+token,
		"LX_TRACE_MAX_BYTES=65536",
	)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	startMarker := "LX_TRACE_START_" + token
	endMarker := "LX_TRACE_END_" + token

	var traces []TraceData
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for sc.Scan() {
		line := sc.Text()

		if strings.HasPrefix(line, startMarker) && strings.HasSuffix(line, endMarker) {
			payload := strings.TrimSuffix(strings.TrimPrefix(line, startMarker), endMarker)

			var td TraceData
			if err := json.Unmarshal([]byte(payload), &td); err == nil {
				td.Function = normalizeFuncName(td.Function)

				if !filepath.IsAbs(td.File) {
					td.File = filepath.Join(dir, td.File)
				}
				td.File = filepath.Clean(td.File)
				traces = append(traces, td)

				valPreview := safeValuePreview(td.Kind, td.Value, 50)
				fmt.Printf("\t[%s] %s: %s\n", td.Kind, td.Function, valPreview)
			}
			continue
		}

		if opts.showStdout {
			fmt.Printf("\t[capture stdout] %s\n", line)
		}
	}

	waitErr := cmd.Wait()
	if scanErr := sc.Err(); scanErr != nil && waitErr == nil {
		waitErr = scanErr
	}

	if ctx.Err() == context.DeadlineExceeded {
		return traces, fmt.Errorf("timeout")
	}

	return traces, waitErr
}

func findMainPackages(root string) ([]string, error) {
	var entryPoints []string
	seen := make(map[string]struct{})

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name == "vendor" || name == ".git" {
				return filepath.SkipDir
			}
			return nil
		}

		if filepath.Ext(path) != ".go" {
			return nil
		}

		dir := filepath.Dir(path)
		if _, ok := seen[dir]; ok {

			return nil
		}

		fset := token.NewFileSet()

		f, err := parser.ParseFile(fset, path, nil, parser.ParseComments|parser.PackageClauseOnly)
		if err != nil {
			return nil
		}

		if f.Name.Name == "main" {
			entryPoints = append(entryPoints, dir)
			seen[dir] = struct{}{}
		}

		return nil
	})

	return entryPoints, err
}

func buildSecureEnvAllowlist() []string {

	allowList := []string{
		"PATH", "HOME", "USER",
		"GOPATH", "GOROOT", "GOMODCACHE",
		"GOPRIVATE", "GOPROXY", "GONOPROXY", "GONOSUMDB",
		"CGO_ENABLED", "GOOS", "GOARCH",

		"TMPDIR",
	}

	var env []string
	for _, key := range allowList {
		if val, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+val)
		}
	}
	return env
}
