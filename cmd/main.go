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
	"syscall"
	"time"

	"google.golang.org/genai"
	"gopkg.in/yaml.v3"
)

var version = "dev"

type Config struct {
	Provider string `yaml:"provider"`
	ApiKey   string `yaml:"api_key"`
	Model    string `yaml:"model"`
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
}

func main() {
	var (
		showVersion bool
		opts        options
	)

	flag.BoolVar(&showVersion, "version", false, "Print version")
	flag.DurationVar(&opts.timeout, "timeout", 2*time.Minute, "Timeout for `go run` capture phase")
	flag.BoolVar(&opts.showStdout, "show-stdout", false, "Print target program stdout (excluding lx trace lines)")
	flag.IntVar(&opts.maxPromptChars, "max-prompt", 4096, "Max characters of prompt sent to LLM (runtime captured input)")
	flag.IntVar(&opts.maxBodyChars, "max-context", 8192, "Max characters of existing function body context sent to LLM")
	flag.IntVar(&opts.maxOutputBytes, "max-output", 32*1024, "Max bytes of sample output JSON sent to LLM")
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
		// Best-effort restore on any unexpected exit path.
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
		fmt.Printf("[lx] Error occurred during execution: %v\n", err)
	}

	fmt.Println("[lx] Analyze the collected data and generating code")
	targets := scanAndMerge(opts.targetDir, traces)
	if len(targets) == 0 {
		fmt.Println("[lx] No conversion target")
		return
	}

	for _, target := range targets {
		processSingleTarget(opts, llm, cfg, target)
	}

	fmt.Println("[lx] All tasks completed")
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
		// Skip symlinks: prevents writing outside root via symlinked files.
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
			// Decls without body: skip safely.
			if fn.Body == nil {
				return true
			}

			// Only functions calling lx.Gen(...) are targets.
			if !hasLxGenCall(fn.Body) {
				return true
			}

			ast.Inspect(fn.Body, func(inner ast.Node) bool {
				retStmt, ok := inner.(*ast.ReturnStmt)
				if !ok {
					return true
				}

				for i, resultExpr := range retStmt.Results {
					if isSpyCall(resultExpr) {
						continue
					}
					spyCall := &ast.CallExpr{
						Fun: &ast.SelectorExpr{
							X:   ast.NewIdent("lx"),
							Sel: ast.NewIdent("Spy"),
						},
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

		// Preserve original mode.
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
			fmt.Printf("[lx] Recovery failed (%s): %v\n", path, err)
		}
	}
}

func walkGoFiles(root string, fn func(path string, d fs.DirEntry) error) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			// Skip vendor/.git directories early for performance.
			name := d.Name()
			if name == "vendor" || name == ".git" {
				return filepath.SkipDir
			}
			return nil
		}

		if filepath.Ext(path) != ".go" {
			return nil
		}
		// Additional guard if path contains vendor/.git fragments.
		if strings.Contains(path, string(filepath.Separator)+"vendor"+string(filepath.Separator)) ||
			strings.Contains(path, string(filepath.Separator)+".git"+string(filepath.Separator)) {
			return nil
		}
		return fn(path, d)
	})
}

func runAndCapture(opts options, dir string) ([]TraceData, error) {
	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()

	goExe, err := exec.LookPath("go")
	if err != nil {
		return nil, fmt.Errorf("go not found in PATH: %w", err)
	}

	absDir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}

	// Use `go run .` with cmd.Dir set: deterministic + avoids path interpretation issues.
	cmd := exec.CommandContext(ctx, goExe, "run", ".")
	cmd.Dir = absDir

	// Secure env: allowlist only.
	secureEnv := buildSecureEnvAllowlist()

	// Token prevents trace injection / spoofing by regular stdout prints.
	token := mustRandomToken(16)

	cmd.Env = append(secureEnv,
		"LX_MODE=capture",
		"LX_TRACE_TOKEN="+token,
		// Bounds trace size to reduce DoS risk from huge values.
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
	// Increase line buffer but keep a hard ceiling.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	for sc.Scan() {
		line := sc.Text()

		if strings.HasPrefix(line, startMarker) && strings.HasSuffix(line, endMarker) {
			payload := strings.TrimSuffix(strings.TrimPrefix(line, startMarker), endMarker)

			var td TraceData
			if err := json.Unmarshal([]byte(payload), &td); err == nil {
				td.Function = normalizeFuncName(td.Function)
				td.File = filepath.Clean(td.File)
				traces = append(traces, td)

				// Pretty terminal log (safe truncation).
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

	// Scanner error (I/O or tokenization limit).
	if scanErr := sc.Err(); scanErr != nil {
		// Prefer command error if any, otherwise return scanner error.
		if waitErr == nil {
			waitErr = scanErr
		}
	}

	if ctx.Err() == context.DeadlineExceeded {
		return traces, fmt.Errorf("capture timed out after %s", opts.timeout)
	}

	return traces, waitErr
}

func buildSecureEnvAllowlist() []string {
	// Minimal env to run `go`.
	allowList := []string{
		"PATH", "HOME", "USER",
		"GOPATH", "GOROOT", "GOMODCACHE",
		"GOPRIVATE", "GOPROXY", "GONOPROXY", "GONOSUMDB",
		"CGO_ENABLED", "GOOS", "GOARCH",
		// Helpful on macOS for toolchain and certificates sometimes:
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
		// If crypto/rand fails, better to hard fail than run without trace auth.
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

	// Normalize target file paths to absolute for robust matching vs runtime.Caller paths.
	for i := range rawTargets {
		if abs, err := filepath.Abs(rawTargets[i].FilePath); err == nil {
			rawTargets[i].FilePath = abs
		}
	}

	// Build index (funcName + filePath) -> *TargetInfo
	index := make(map[string]*TargetInfo, len(rawTargets))
	finalTargets := make([]TargetInfo, 0, len(rawTargets))

	for _, rt := range rawTargets {
		rtCopy := rt
		key := rtCopy.FuncName + "\n" + rtCopy.FilePath
		index[key] = &rtCopy
		finalTargets = append(finalTargets, rtCopy)
	}

	// Apply traces to targets
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
				// fallback
				target.Output = string(t.Value)
			}
		}
	}

	// Rebuild final slice from index-updated pointers (keep order)
	out := make([]TargetInfo, 0, len(finalTargets))
	for _, rt := range finalTargets {
		key := rt.FuncName + "\n" + rt.FilePath
		cur := index[key]
		if cur == nil {
			continue
		}
		if cur.Output != "" {
			fmt.Printf("\t[Data] %s: Input=\"%s\", Output=Confirmed\n", cur.FuncName, truncateString(cur.Prompt, 80))
		}
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
						// Prefer string literal.
						if lit, ok := call.Args[0].(*ast.BasicLit); ok && lit.Kind == token.STRING {
							prompt = strings.Trim(lit.Value, "`\"")
						}
						// Fallback to expression.
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

func processSingleTarget(opts options, llm LLM, cfg *Config, target TargetInfo) {
	displayPath := target.FilePath
	taskName := fmt.Sprintf("[%s -> %s]", displayPath, target.FuncName)
	fmt.Printf("[lx] %s Generate code\n", taskName)

	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, target.FilePath, nil, parser.ParseComments)
	if err != nil {
		fmt.Printf("[lx] %s parse failed: %v\n", taskName, err)
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
		fmt.Printf("[lx] %s function not found or has no body\n", taskName)
		return
	}

	currentBody := extractBody(fset, currentFn)
	signature := extractSignature(fset, currentFn)

	// Bound what we send to the LLM (reduces accidental exfil + cost + prompt injection surface).
	prompt := truncateString(singleLine(target.Prompt), opts.maxPromptChars)
	bodyCtx := truncateString(currentBody, opts.maxBodyChars)

	outputSection := ""
	if target.Output != "" {
		outBytes := []byte(target.Output)
		if len(outBytes) > opts.maxOutputBytes {
			outBytes = append(outBytes[:opts.maxOutputBytes], []byte("\n... [truncated]")...)
		}
		outputSection = fmt.Sprintf(
			"\n[SAMPLE OUTPUT (Runtime Captured)]\nThis function MUST return a structure/value similar to this:\n%s\n",
			string(outBytes),
		)
	}

	systemPrompt := fmt.Sprintf(`GO FUNC BODY GEN.

SIG: %s

TASK: %s

CONTEXT(DUMMY): %s

%s

RULES:
1. OUTPUT BODY ONLY.
2. NO MARKDOWN.
3. NO "lx.Gen".
4. NEVER add network calls or file I/O unless explicitly required by TASK.
5. USE // lx-dep: for any new imports/packages you use.`, signature, prompt, bodyCtx, outputSection)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	generatedCode, err := llm.Generate(ctx, cfg.Model, systemPrompt)
	if err != nil {
		fmt.Printf("[lx] %s code generation failed\n", taskName)
		fmt.Printf("[lx] Error: %s\n", diagnoseLLMError(err))
		return
	}

	cleaned := cleanAICode(generatedCode)
	deps := extractDependencies(cleaned)

	if ok := applyCodeToFile(target.FilePath, currentFn, fset, prompt, cleaned); ok {
		fmt.Printf("[lx] %s complete\n", taskName)
		if len(deps) > 0 {
			fmt.Printf("[lx] %s deps (manual): %s\n", taskName, strings.Join(uniqueStrings(deps), ", "))
		}
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

// -------- LLM abstraction (extensible) --------

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
	if strings.TrimSpace(cfg.ApiKey) == "" {
		return nil, errors.New("empty api_key")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("empty model")
	}
	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	if provider == "" {
		provider = "gemini"
	}
	if provider != "gemini" {
		return nil, fmt.Errorf("unsupported provider: %s (only gemini)", cfg.Provider)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	client, err := genai.NewClient(ctx, &genai.ClientConfig{APIKey: cfg.ApiKey})
	if err != nil {
		return nil, err
	}
	return &geminiLLM{client: client}, nil
}

func (g *geminiLLM) Generate(ctx context.Context, model string, prompt string) (string, error) {
	resp, err := g.client.Models.GenerateContent(ctx, model, genai.Text(prompt), nil)
	if err != nil {
		return "", err
	}
	return resp.Text(), nil
}

// -------- helpers / parsing --------

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
	code = strings.TrimSpace(code)

	// Strip fenced blocks if any.
	code = strings.TrimPrefix(code, "```go")
	code = strings.TrimPrefix(code, "```")
	code = strings.TrimSuffix(code, "```")
	code = strings.TrimSpace(code)

	// Some models wrap in braces; tolerate but do not assume it's always present.
	if strings.HasPrefix(code, "{") && strings.HasSuffix(code, "}") {
		code = strings.TrimSpace(code[1 : len(code)-1])
	}

	lines := strings.Split(code, "\n")
	cleanLines := make([]string, 0, len(lines))
	for _, line := range lines {
		// Never allow lx.Gen to survive into generated body.
		if strings.Contains(line, "lx.Gen(") {
			continue
		}
		cleanLines = append(cleanLines, line)
	}
	return strings.TrimSpace(strings.Join(cleanLines, "\n"))
}

func applyCodeToFile(path string, fn *ast.FuncDecl, fset *token.FileSet, prompt, generated string) bool {
	// Preserve original mode
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

	// Prompt comment: keep single line, avoid accidental multiline comment injection.
	cleanPrompt := sanitizeComment(prompt)
	finalBody := fmt.Sprintf("{\n\t// lx-prompt: %s\n\n%s\n}", cleanPrompt, generated)

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

	// Only gofmt. goimports is intentionally NOT run by lx (developer retains full control).
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
	// Prevent closing comment tokens or weird control chars from breaking formatting.
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
