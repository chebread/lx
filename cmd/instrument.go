package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

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
