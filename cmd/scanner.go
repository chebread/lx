package main

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

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
