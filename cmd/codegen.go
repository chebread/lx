package main

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
)

var logMu sync.Mutex

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

func runTool(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
