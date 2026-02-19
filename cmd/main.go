package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"google.golang.org/genai"
	"gopkg.in/yaml.v3"
)

var version = "dev"

type Config struct {
	Provider string   `yaml:"provider"`
	ApiKey   string   `yaml:"api_key"`
	Model    string   `yaml:"model"`
	BinPath  string   `yaml:"bin_path"`
	Args     []string `yaml:"args"`
}

type TargetInfo struct {
	FilePath string
	FuncName string
	Prompt   string
	Output   string
}

type TraceData struct {
	Kind     string          `json:"kind"`
	Function string          `json:"function"`
	Value    json.RawMessage `json:"value"`
	File     string          `json:"file"`
	Line     int             `json:"line"`
}

type fileBackup struct {
	Data []byte
	Mode fs.FileMode
}

type options struct {
	targetDir      string
	timeout        time.Duration
	showStdout     bool
	maxPromptChars int
	maxBodyChars   int
	maxOutputBytes int
	tags           string
}

type commandLLM struct {
	binPath string
	args    []string
}

var logMu sync.Mutex

func main() {
	startTime := time.Now()

	var (
		showVersion bool
		opts        options
	)

	flag.BoolVar(&showVersion, "version", false, "Print version")
	flag.DurationVar(&opts.timeout, "timeout", 5*time.Minute, "Timeout for `go run` capture phase")
	flag.BoolVar(&opts.showStdout, "show-stdout", false, "Print target program stdout (excluding lx trace lines)")
	flag.IntVar(&opts.maxPromptChars, "max-prompt", 4096, "Max characters of prompt sent to LLM (runtime captured input)")
	flag.IntVar(&opts.maxBodyChars, "max-context", 8192, "Max characters of existing function body context sent to LLM")
	flag.IntVar(&opts.maxOutputBytes, "max-output", 32*1024, "Max bytes of sample output JSON sent to LLM")
	flag.StringVar(&opts.tags, "tags", "", "Build tags to pass to `go run` capture phase (e.g. 'mock')")
	flag.Parse()

	if showVersion {
		fmt.Printf("lx %s\n", version)
		return
	}

	opts.targetDir = "."
	if args := flag.Args(); len(args) > 0 {
		opts.targetDir = args[0]
	}

	cfg, configInfo, err := loadConfig()
	if err != nil {
		log.Fatalf("[lx] Config Error: %v", err)
	}

	llm, err := newLLM(cfg)
	if err != nil {
		log.Fatalf("[lx] LLM init error: %v", err)
	}

	fmt.Println("[lx] Start running...")
	fmt.Printf("[lx] Config: %s\n", configInfo)
	fmt.Printf("[lx] Provider: [%s] / Model: [%s]\n", cfg.Provider, cfg.Model)

	fmt.Println("[lx] Converting code")
	backups, err := injectSpyCode(opts.targetDir)
	if err != nil {
		fmt.Printf("[lx] Conversion failed: %v\n", err)
		revertCode(backups)
		return
	}

	setupSafeExit(backups)

	defer func() {

		if len(backups) > 0 {
			revertCode(backups)
		}
	}()

	fmt.Println("[lx] Run the program and collect data")
	traces, err := runAndCapture(opts, opts.targetDir)

	fmt.Println("[lx] Restore the source code")
	revertCode(backups)
	clear(backups)

	if err != nil {
		revertCode(backups)
		log.Fatalf("\n[lx] Stop: Execution failed. Fix your Go code first.\nError: %v", err)
	}

	fmt.Println("[lx] Analyze the collected data and generating code")
	targets := scanAndMerge(opts.targetDir, traces)
	if len(targets) == 0 {
		fmt.Println("[lx] No conversion target")
		return
	}

	var wg sync.WaitGroup

	semaphore := make(chan struct{}, 2)

	fileLocks := make(map[string]*sync.Mutex)
	for _, t := range targets {
		if _, exists := fileLocks[t.FilePath]; !exists {
			fileLocks[t.FilePath] = &sync.Mutex{}
		}
	}

	for _, target := range targets {
		wg.Add(1)

		go func(t TargetInfo) {
			defer wg.Done()

			semaphore <- struct{}{}
			defer func() { <-semaphore }()

			fileMu := fileLocks[t.FilePath]

			processSingleTarget(opts, llm, cfg, t, fileMu)
		}(target)
	}

	wg.Wait()

	elapsed := time.Since(startTime)
	fmt.Printf("[lx] All tasks completed in %s\n", elapsed)
}

func setupSafeExit(backups map[string]fileBackup) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-c
		fmt.Println("\n[lx] Forced termination detected. Restoring source code...")
		revertCode(backups)
		os.Exit(1)
	}()
}

func injectSpyCode(root string) (map[string]fileBackup, error) {
	backups := make(map[string]fileBackup)

	err := walkGoFiles(root, func(path string, d fs.DirEntry) error {

		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil
		}

		src, err := os.ReadFile(path)
		if err != nil {
			return nil
		}

		fset := token.NewFileSet()
		node, err := parser.ParseFile(fset, path, src, parser.ParseComments)
		if err != nil {
			return nil
		}

		modified := false

		ast.Inspect(node, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok {
				return true
			}
			if fn.Body == nil {
				return true
			}

			if !hasLxGenCall(fn.Body) {
				return true
			}

			var returnTypes []ast.Expr
			if fn.Type.Results != nil {
				for _, field := range fn.Type.Results.List {
					count := len(field.Names)
					if count == 0 {
						count = 1
					}
					for i := 0; i < count; i++ {
						returnTypes = append(returnTypes, field.Type)
					}
				}
			}

			isVoid := len(returnTypes) == 0

			if isVoid {
				deferStmt := &ast.DeferStmt{
					Call: &ast.CallExpr{
						Fun: &ast.SelectorExpr{
							X:   ast.NewIdent("lx"),
							Sel: ast.NewIdent("SpyVoid"),
						},
						Args: []ast.Expr{
							&ast.BasicLit{
								Kind:  token.STRING,
								Value: fmt.Sprintf("%q", fn.Name.Name),
							},
						},
					},
				}
				fn.Body.List = append([]ast.Stmt{deferStmt}, fn.Body.List...)
				modified = true
			} else {
				ast.Inspect(fn.Body, func(inner ast.Node) bool {
					retStmt, ok := inner.(*ast.ReturnStmt)
					if !ok {
						return true
					}

					for i, resultExpr := range retStmt.Results {
						if i >= len(returnTypes) || isSpyCall(resultExpr) {
							continue
						}

						spySelector := &ast.SelectorExpr{
							X:   ast.NewIdent("lx"),
							Sel: ast.NewIdent("Spy"),
						}

						spyInstance := &ast.IndexExpr{
							X:     spySelector,
							Index: returnTypes[i],
						}

						spyCall := &ast.CallExpr{
							Fun: spyInstance,
							Args: []ast.Expr{
								&ast.BasicLit{
									Kind:  token.STRING,
									Value: fmt.Sprintf("%q", fn.Name.Name),
								},
								resultExpr,
							},
						}
						retStmt.Results[i] = spyCall
						modified = true
					}
					return true
				})
			}

			return true
		})

		if !modified {
			return nil
		}

		backups[path] = fileBackup{Data: src, Mode: info.Mode()}

		var buf bytes.Buffer
		if err := format.Node(&buf, fset, node); err != nil {
			return err
		}

		if err := os.WriteFile(path, buf.Bytes(), info.Mode()); err != nil {
			return err
		}

		return nil
	})

	return backups, err
}

func revertCode(backups map[string]fileBackup) {
	for path, b := range backups {
		if err := os.WriteFile(path, b.Data, b.Mode); err != nil {
			fmt.Printf("[lx] [Error] Recovery failed (%s): %v\n", path, err)
		}
	}
}

func walkGoFiles(root string, fn func(path string, d fs.DirEntry) error) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
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

		if strings.Contains(path, string(filepath.Separator)+"vendor"+string(filepath.Separator)) ||
			strings.Contains(path, string(filepath.Separator)+".git"+string(filepath.Separator)) {
			return nil
		}
		return fn(path, d)
	})
}

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

func mustRandomToken(nBytes int) string {
	b := make([]byte, nBytes)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {

		panic(err)
	}
	return hex.EncodeToString(b)
}

func normalizeFuncName(full string) string {
	if idx := strings.LastIndex(full, "."); idx != -1 {
		return full[idx+1:]
	}
	return full
}

func safeValuePreview(kind string, raw json.RawMessage, max int) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		s := string(raw)
		return truncateString(s, max)
	}
	s := fmt.Sprintf("%v", v)
	return truncateString(s, max)
}

func truncateString(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}

func scanAndMerge(root string, traces []TraceData) []TargetInfo {
	rawTargets := scanProjectForLx(root)

	for i := range rawTargets {
		if abs, err := filepath.Abs(rawTargets[i].FilePath); err == nil {
			rawTargets[i].FilePath = abs
		}
	}

	index := make(map[string]*TargetInfo, len(rawTargets))
	finalTargets := make([]TargetInfo, 0, len(rawTargets))

	for _, rt := range rawTargets {
		rtCopy := rt
		key := rtCopy.FuncName + "\n" + rtCopy.FilePath
		index[key] = &rtCopy
		finalTargets = append(finalTargets, rtCopy)
	}

	for _, t := range traces {
		tf := t.File
		if abs, err := filepath.Abs(tf); err == nil {
			tf = abs
		}
		key := t.Function + "\n" + tf
		target := index[key]
		if target == nil {
			continue
		}

		switch t.Kind {
		case "INPUT":
			var s string
			if err := json.Unmarshal(t.Value, &s); err == nil && s != "" {
				target.Prompt = s
			} else {
				target.Prompt = string(t.Value)
			}
		case "OUTPUT":
			var anyVal any
			if err := json.Unmarshal(t.Value, &anyVal); err == nil {
				if pretty, err := json.MarshalIndent(anyVal, "", "  "); err == nil {
					target.Output = string(pretty)
				}
			} else {

				target.Output = string(t.Value)
			}
		}
	}

	out := make([]TargetInfo, 0, len(finalTargets))
	for _, rt := range finalTargets {
		key := rt.FuncName + "\n" + rt.FilePath
		cur := index[key]
		if cur == nil || cur.Output == "" {
			continue
		}

		fmt.Printf("\t[Data] %s: Input=\"%s\", Output=Confirmed\n", cur.FuncName, truncateString(cur.Prompt, 80))
		out = append(out, *cur)
	}
	return out
}

func scanProjectForLx(root string) []TargetInfo {
	var targets []TargetInfo

	_ = walkGoFiles(root, func(path string, d fs.DirEntry) error {
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}

		abs, err := filepath.Abs(path)
		if err != nil {
			return nil
		}

		fset := token.NewFileSet()
		node, err := parser.ParseFile(fset, abs, nil, parser.ParseComments)
		if err != nil {
			return nil
		}

		ast.Inspect(node, func(n ast.Node) bool {
			fn, ok := n.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				return true
			}

			ast.Inspect(fn.Body, func(inner ast.Node) bool {
				call, ok := inner.(*ast.CallExpr)
				if !ok {
					return true
				}

				if isLxGenCall(call) {
					prompt := ""
					if len(call.Args) > 0 {

						if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
							prompt = strings.Trim(lit.Value, "`\"")
						}

						if prompt == "" {
							prompt = nodeToString(fset, call.Args[0])
						}
					}

					if prompt != "" {
						targets = append(targets, TargetInfo{
							FilePath: abs,
							FuncName: fn.Name.Name,
							Prompt:   prompt,
						})
					}
				}
				return true
			})

			return true
		})

		return nil
	})

	return targets
}

func processSingleTarget(opts options, llm LLM, cfg *Config, target TargetInfo, fileMu *sync.Mutex) {
	displayPath := target.FilePath
	taskName := fmt.Sprintf("[%s -> %s]", displayPath, target.FuncName)

	logMu.Lock()
	fmt.Printf("[lx] %s Generate code\n", taskName)
	logMu.Unlock()

	fileMu.Lock()

	fset := token.NewFileSet()

	node, err := parser.ParseFile(fset, target.FilePath, nil, parser.ParseComments)
	if err != nil {
		fileMu.Unlock()
		logMu.Lock()
		fmt.Printf("[lx] %s parse failed: %v\n", taskName, err)
		logMu.Unlock()
		return
	}

	var currentFn *ast.FuncDecl
	ast.Inspect(node, func(n ast.Node) bool {
		if fn, ok := n.(*ast.FuncDecl); ok && fn.Name.Name == target.FuncName {
			currentFn = fn
			return false
		}
		return true
	})

	if currentFn == nil || currentFn.Body == nil {
		fileMu.Unlock()
		logMu.Lock()
		fmt.Printf("[lx] %s function not found or has no body\n", taskName)
		logMu.Unlock()
		return
	}

	signature := extractSignature(fset, currentFn)

	fileMu.Unlock()

	prompt := truncateString(singleLine(target.Prompt), opts.maxPromptChars)
	isVoid := currentFn.Type.Results == nil || len(currentFn.Type.Results.List) == 0

	outputSection := ""
	if isVoid {
		outputSection = "\n[VOID FUNCTION]\nThis function has NO return values. Focus strictly on logic and side effects (printing, etc).\n"
	} else {
		var retTypes []string
		for _, field := range currentFn.Type.Results.List {
			retTypes = append(retTypes, nodeToString(fset, field.Type))
		}
		retTypeStr := strings.Join(retTypes, ", ")

		outputSection = fmt.Sprintf("\n[RETURN VALUES REQUIRED]\nThis function MUST return values of type: (%s)\n", retTypeStr)

		if target.Output != "" && target.Output != "null" && target.Output != "<nil>" {
			outBytes := []byte(target.Output)
			if len(outBytes) > opts.maxOutputBytes {
				outBytes = append(outBytes[:opts.maxOutputBytes], []byte("\n... [truncated]")...)
			}
			outputSection += fmt.Sprintf("Captured sample output shape:\n%s\n", string(outBytes))
		} else {
			outputSection += "Note: The trace run returned nil or empty, but you MUST still provide a valid return statement matching the signature.\n"
		}
	}

	systemPrompt := fmt.Sprintf(`GO FUNC BODY GEN.

SIG: %s

TASK: %s

%s

RULES:
1. OUTPUT BODY ONLY. Do NOT include the "func Name() {" line.
2. NO MARKDOWN.
3. NO "lx.Gen".
4. NEVER add network calls or file I/O unless explicitly required by TASK.
5. USE // lx-dep: for any new imports/packages you use.
6. START directly with logic.
7. COMPLIANCE: If the function signature has return types, you MUST include a return statement.`, signature, prompt, outputSection)

	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()

	generatedCode, err := llm.Generate(ctx, cfg.Model, systemPrompt)
	if err != nil {
		logMu.Lock()
		fmt.Printf("[lx] %s code generation failed\n", taskName)
		fmt.Printf("[lx] Error: %s\n", diagnoseLLMError(err))
		logMu.Unlock()
		return
	}

	cleaned := cleanAICode(generatedCode)
	deps := extractDependencies(cleaned)

	fileMu.Lock()
	defer fileMu.Unlock()

	freshFset := token.NewFileSet()
	freshNode, err := parser.ParseFile(freshFset, target.FilePath, nil, parser.ParseComments)
	if err != nil {
		fmt.Printf("[lx] %s re-parse failed: %v\n", taskName, err)
		return
	}

	var freshFn *ast.FuncDecl
	ast.Inspect(freshNode, func(n ast.Node) bool {
		if fn, ok := n.(*ast.FuncDecl); ok && fn.Name.Name == target.FuncName {
			freshFn = fn
			return false
		}
		return true
	})

	if freshFn == nil || freshFn.Body == nil {
		fmt.Printf("[lx] %s function not found during re-parse\n", taskName)
		return
	}

	if ok := applyCodeToFile(target.FilePath, freshFn, freshFset, prompt, cleaned); ok {
		logMu.Lock()
		fmt.Printf("[lx] %s complete\n", taskName)
		if len(deps) > 0 {
			fmt.Printf("[lx] %s deps (manual): %s\n", taskName, strings.Join(uniqueStrings(deps), ", "))
		}
		logMu.Unlock()
	}
}

func uniqueStrings(in []string) []string {
	seen := make(map[string]struct{}, len(in))
	var out []string
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

func singleLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}

func diagnoseLLMError(err error) string {
	msg := err.Error()

	switch {
	case strings.Contains(msg, "timeout reached"):
		return fmt.Sprintf("TIMEOUT: The operation exceeded the time limit. (%s)", msg)

	case strings.Contains(msg, "API_KEY_INVALID"):
		return "The API key is incorrect. Please double-check the api_key in 'lx-config.yaml'."
	case strings.Contains(msg, "quota"):
		return "You have exceeded your API call quota. Please try again later or check your payment information."
	case strings.Contains(msg, "model not found"):
		return "The specified model could not be found. Please verify that the model name is correct."
	case strings.Contains(msg, "safety"):
		return "Your response has been blocked by security policy. Please edit the prompt."
	case strings.Contains(msg, "connection") || strings.Contains(msg, "timeout"):
		return "The network connection is unstable. Please check your Internet connection."

	default:
		return fmt.Sprintf("An unknown error has occurred: %v", err)
	}
}

type LLM interface {
	Generate(ctx context.Context, model string, prompt string) (string, error)
}

type geminiLLM struct {
	client *genai.Client
}

func newLLM(cfg *Config) (LLM, error) {
	if cfg == nil {
		return nil, errors.New("nil config")
	}

	if strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("empty model")
	}

	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	if provider == "" {
		provider = "gemini"
	}

	switch provider {
	case "gemini":
		if strings.TrimSpace(cfg.ApiKey) == "" {
			return nil, errors.New("empty api_key")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		client, err := genai.NewClient(ctx, &genai.ClientConfig{APIKey: cfg.ApiKey})
		if err != nil {
			return nil, err
		}
		return &geminiLLM{client: client}, nil

	case "command":
		if strings.TrimSpace(cfg.BinPath) == "" {
			return nil, errors.New("empty bin_path (required for command provider)")
		}

		return &commandLLM{
			binPath: cfg.BinPath,
			args:    cfg.Args,
		}, nil

	default:
		return nil, fmt.Errorf("unsupported provider: %s", cfg.Provider)
	}
}

func (g *geminiLLM) Generate(ctx context.Context, model string, prompt string) (string, error) {
	resp, err := g.client.Models.GenerateContent(ctx, model, genai.Text(prompt), nil)
	if err != nil {
		return "", err
	}
	return resp.Text(), nil
}

func (c *commandLLM) Generate(ctx context.Context, model string, prompt string) (string, error) {
	var finalArgs []string

	if len(c.args) == 0 {
		finalArgs = []string{"-p", prompt, "-m", model, "-o", "text"}
	} else {
		for _, arg := range c.args {
			replaced := strings.ReplaceAll(arg, "{{prompt}}", prompt)
			replaced = strings.ReplaceAll(replaced, "{{model}}", model)
			finalArgs = append(finalArgs, replaced)
		}
	}

	cmd := exec.CommandContext(ctx, c.binPath, finalArgs...)

	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("timeout reached (%s): process group killed", ctx.Err())
		}
		return "", fmt.Errorf("command execution failed: %v\nStderr: %s", err, stderr.String())
	}

	return out.String(), nil
}

func hasLxGenCall(body *ast.BlockStmt) bool {
	if body == nil {
		return false
	}
	found := false
	ast.Inspect(body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if isLxGenCall(call) {
			found = true
			return false
		}
		return true
	})
	return found
}

func isSpyCall(expr ast.Expr) bool {
	call, ok := expr.(*ast.CallExpr)
	if !ok {
		return false
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	return ok && x.Name == "lx" && sel.Sel.Name == "Spy"
}

func loadConfig() (*Config, string, error) {
	localPath := "lx-config.yaml"
	if _, err := os.Stat(localPath); err == nil {
		data, err := os.ReadFile(localPath)
		if err != nil {
			return nil, "", err
		}
		var cfg Config
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return nil, "", err
		}
		return &cfg, "./lx-config.yaml [Local]", nil
	}

	home, err := os.UserHomeDir()
	if err == nil {
		globalPath := filepath.Join(home, "lx-config.yaml")
		if _, err := os.Stat(globalPath); err == nil {
			data, err := os.ReadFile(globalPath)
			if err != nil {
				return nil, "", err
			}
			var cfg Config
			if err := yaml.Unmarshal(data, &cfg); err != nil {
				return nil, "", err
			}
			displayPath := strings.Replace(globalPath, home, "~", 1)
			return &cfg, fmt.Sprintf("%s [Global]", displayPath), nil
		}
	}

	return nil, "", fmt.Errorf("could not find 'lx-config.yaml' file")
}

func cleanAICode(code string) string {
	if start := strings.Index(code, "```"); start != -1 {
		if firstNL := strings.Index(code[start:], "\n"); firstNL != -1 {
			content := code[start+firstNL+1:]
			if last := strings.LastIndex(content, "```"); last != -1 {
				code = content[:last]
			}
		}
	}

	if strings.Contains(code, "func ") && strings.Contains(code, "{") {
		if open := strings.Index(code, "{"); open != -1 {
			if close := strings.LastIndex(code, "}"); close != -1 {
				code = code[open+1 : close]
			}
		}
	}

	trimmed := strings.TrimSpace(code)
	if strings.HasPrefix(trimmed, "{") && strings.HasSuffix(trimmed, "}") {
		code = trimmed[1 : len(trimmed)-1]
	}

	lines := strings.Split(code, "\n")
	var finalLines []string
	for _, line := range lines {
		t := strings.TrimSpace(line)
		if t == "" || strings.Contains(t, "lx.Gen(") {
			continue
		}
		finalLines = append(finalLines, line)
	}

	return strings.Join(finalLines, "\n")
}

func applyCodeToFile(path string, fn *ast.FuncDecl, fset *token.FileSet, prompt, generated string) bool {

	info, err := os.Stat(path)
	if err != nil {
		fmt.Printf("[lx] stat failed: %v\n", err)
		return false
	}

	src, err := os.ReadFile(path)
	if err != nil {
		fmt.Printf("[lx] read failed: %v\n", err)
		return false
	}

	cleanPrompt := sanitizeComment(prompt)
	finalBody := fmt.Sprintf("{\n\t// lx-prompt: %s\n\t%s\n}",
		cleanPrompt,
		strings.ReplaceAll(generated, "\n", "\n\t"),
	)

	startOffset := fset.Position(fn.Body.Pos()).Offset
	endOffset := fset.Position(fn.Body.End()).Offset
	if startOffset < 0 || endOffset < 0 || startOffset > len(src) || endOffset > len(src) || startOffset > endOffset {
		fmt.Printf("[lx] invalid offsets for %s\n", path)
		return false
	}

	newSrc := append([]byte{}, src[:startOffset]...)
	newSrc = append(newSrc, []byte(finalBody)...)
	newSrc = append(newSrc, src[endOffset:]...)

	if err := os.WriteFile(path, newSrc, info.Mode()); err != nil {
		fmt.Printf("[lx] write failed: %v\n", err)
		return false
	}

	if err := runTool("gofmt", "-w", path); err != nil {
		fmt.Printf("[lx] gofmt warning: %v\n", err)
	}

	return true
}

func runTool(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func sanitizeComment(s string) string {
	s = singleLine(s)
	s = strings.ReplaceAll(s, "*/", "* /")
	s = strings.ReplaceAll(s, "//", "/ /")
	return s
}

func isLxGenCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	x, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return x.Name == "lx" && sel.Sel.Name == "Gen"
}

func nodeToString(fset *token.FileSet, node ast.Node) string {
	var buf bytes.Buffer
	_ = format.Node(&buf, fset, node)
	return buf.String()
}

func extractSignature(fset *token.FileSet, fn *ast.FuncDecl) string {
	var buf bytes.Buffer
	body := fn.Body
	fn.Body = nil
	_ = format.Node(&buf, fset, fn)
	fn.Body = body
	return buf.String()
}

func extractBody(fset *token.FileSet, fn *ast.FuncDecl) string {
	var buf bytes.Buffer
	if fn.Body != nil {
		_ = format.Node(&buf, fset, fn.Body)
	}
	return buf.String()
}

func extractDependencies(code string) []string {
	re := regexp.MustCompile(`(?i)//\s*lx-dep:\s*([^\s\n]+)`)
	matches := re.FindAllStringSubmatch(code, -1)
	var deps []string
	for _, m := range matches {
		if len(m) > 1 {
			deps = append(deps, m[1])
		}
	}
	return deps
}
