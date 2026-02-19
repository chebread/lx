package lx

import (
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
)

var traceMu sync.Mutex

type tracePayload struct {
	Kind     string      `json:"kind"`
	Function string      `json:"function"`
	Value    interface{} `json:"value"`
	File     string      `json:"file"`
	Line     int         `json:"line"`
}

// Gen captures the prompt at runtime when LX_MODE=capture and LX_TRACE_TOKEN is set.
// Otherwise it is a no-op.
func Gen(prompt string) {
	if os.Getenv("LX_MODE") != "capture" {
		return
	}
	token := os.Getenv("LX_TRACE_TOKEN")
	if token == "" {
		// Safe default: if token missing, do not emit traces.
		return
	}

	pc, file, line, ok := runtime.Caller(1)
	if !ok {
		return
	}
	fn := runtime.FuncForPC(pc)
	if fn == nil {
		return
	}

	sendTrace(token, tracePayload{
		Kind:     "INPUT",
		Function: fn.Name(),
		Value:    prompt,
		File:     file,
		Line:     line,
	})
}

// Spy captures return values at runtime when LX_MODE=capture and LX_TRACE_TOKEN is set.
// Otherwise it returns val unchanged.
func Spy[T any](funcName string, val T) T {
	if os.Getenv("LX_MODE") != "capture" {
		return val
	}
	token := os.Getenv("LX_TRACE_TOKEN")
	if token == "" {
		return val
	}

	_, file, line, _ := runtime.Caller(1)

	sendTrace(token, tracePayload{
		Kind:     "OUTPUT",
		Function: funcName,
		Value:    val,
		File:     file,
		Line:     line,
	})

	return val
}

func sendTrace(token string, p tracePayload) {
	// Optional bound to prevent huge trace lines (DoS risk).
	maxBytes := traceMaxBytes()

	// Marshal once; if too big, replace Value with a compact summary.
	b, err := json.Marshal(p)
	if err != nil {
		return
	}
	if maxBytes > 0 && len(b) > maxBytes {
		p.Value = fmt.Sprintf("[lx] value omitted (trace %d bytes > max %d)", len(b), maxBytes)
		b, err = json.Marshal(p)
		if err != nil {
			return
		}
	}

	start := "LX_TRACE_START_" + token
	end := "LX_TRACE_END_" + token

	// Mutex reduces interleaving from concurrent goroutines.
	traceMu.Lock()
	defer traceMu.Unlock()

	// Single line output for robust scanner parsing.
	fmt.Printf("%s%s%s\n", start, string(b), end)
}

func traceMaxBytes() int {
	// Default 64KB.
	def := 64 * 1024
	s := strings.TrimSpace(os.Getenv("LX_TRACE_MAX_BYTES"))
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func SpyVoid(funcName string) {
	if os.Getenv("LX_MODE") != "capture" {
		return
	}
	token := os.Getenv("LX_TRACE_TOKEN")
	if token == "" {
		return
	}

	_, file, line, _ := runtime.Caller(1)

	// Value에 nil을 명시적으로 넣습니다.
	sendTrace(token, tracePayload{
		Kind:     "OUTPUT",
		Function: funcName,
		Value:    nil, // JSON으로 변환되면 null이 됩니다.
		File:     file,
		Line:     line,
	})
}
