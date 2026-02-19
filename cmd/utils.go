package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/format"
	"go/token"
	"io"
	"strings"
)

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

func singleLine(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
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

func sanitizeComment(s string) string {
	s = singleLine(s)
	s = strings.ReplaceAll(s, "*/", "* /")
	s = strings.ReplaceAll(s, "//", "/ /")
	return s
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
