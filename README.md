# lx: Controllable AI for Gophers
`lx` restores the [joy of wrangling code](https://tidyfirst.substack.com/p/augmented-coding-beyond-the-vibes) by letting you define the forest, while AI plants the trees under your intentional command.

---

# Table of Contents
- [Philosophy](#philosophy)
- [Feature](#feature)
- [Usage](#usage)
- [Installation & Config](#installation--config)
- [License](#license)

---

# Philosophy
Some argue that Vibe Coding is just a natural evolution like the shift from punch cards to Assembly, and then to C.
I fundamentally disagree.
Historical abstractions were about placing a better hammer in the developer's hand.
The tools evolved, but the wielder remained human. The outcome was always under our absolute control.
Today's Vibe Coding is different.
The robot has taken the hammer. We’ve been sidelined, left to do nothing but beg the machine for the right output.
If we do not wield the hammer, we are no longer makers.
If it is not under our control, it's not programming anymore.
That is why I built `lx`.
`lx` puts the hammer back in the Gopher's hand, restoring absolute control.
It allows you to command AI strictly on your terms through a new paradigm: LLM Functionalization.
It all works in your editor, on your machine, under your terms.

---

# Feature

## Developer Sovereignty

The function signature is the contract. `lx` treats your function names, parameters, and return types as an immutable contract. AI cannot change your architecture; it can only fulfill the implementation details you demand.

### Example

```go
// You define the exact inputs and outputs.
func CalculateDiscount(price float64, customerType string) (float64, error) {
    // Intent: You command the AI.
    lx.Gen("Calculate a 15% discount for 'Premium' customers, otherwise 0%.")

    // Output: The dummy return ensures the contract is maintained.
    // lx will replace the body but strictly adhere to returning (float64, error).
    return 0.0, nil 
}

```

## Deterministic AI Coding

`lx` doesn't just read your code; it runs it. If a function is not reached during execution, `lx` touches nothing. It strictly adheres to the Go runtime path. No hallucinations for dead code, no assumptions about unused functions.

### Example

```go
func main() {
    // lx traces this execution path. It captures the real runtime data.
    GenerateReport([]string{"sales", "marketing"})

    if false {
        // lx ignores this entirely. 
        // No assumptions, no hallucinations for dead code.
        LegacyCleanup()
    }
}

func GenerateReport(data []string) string { 
    lx.Gen("Format the data slice into a comma-separated string report.")
    return "dummy-report" 
}

func LegacyCleanup() { 
    // This lx.Gen is never triggered during the capture phase.
    lx.Gen("Delete all temporary files.")
}

```

## Continuous Development

With `lx`, once you write a function’s inputs and outputs, you can keep moving as if the logic is already there. But it’s not "design-only" — your program still has to compile and run. You drive the flow and the contract; `lx` uses runtime traces to fill in the function internals.

### Example

```go
// 1. You write the higher-level flow as if the logic is already complete.
func ProcessUser(rawName string) error {
    // 2. You establish the data flow. The program compiles and runs.
    sanitized := SanitizeInput(rawName) 
    return SaveToDB(sanitized)
}

// 3. You define the contracts using lx.Gen and keep moving.
func SanitizeInput(input string) string {
    lx.Gen("Trim leading/trailing spaces and convert to Title Case.")
    return "DummyString"
}

func SaveToDB(name string) error {
    lx.Gen("Log the name being saved, then return nil.")
    return nil
}

```

## Safe Recovery
`lx` modifies your source code to inject trace hooks for runtime capture. If your program panics, encounters a fatal error, or you manually abort the process (`Ctrl+C`), `lx` intercepts the OS signals (SIGTERM/SIGINT). It guarantees a clean rollback, instantly reverting your files to their exact original state. Your codebase is never left broken or polluted with spy code.

### Example

Even if your orchestration logic contains a fatal flaw, `lx` protects your source files.

```go
func main() {
    // You are building your flow, but accidentally introduce a bug.
    // This will crash during the 'lx' capture execution.
    TriggerFatalError()

    // lx never reaches this because the program panicked.
    LX_ProcessData("data")
}

func TriggerFatalError() {
    var ptr *int
    fmt.Println(*ptr) // Panic: nil pointer dereference
}

func LX_ProcessData(input string) string {
    lx.Gen("Process the input string safely.")
    return ""
}

```

When you run `lx`, it catches the crash, halts the AI generation process, and immediately cleans up the injected trace hooks:

```bash
$ lx .
[lx] Start running...
[lx] Converting code
[lx] Run the program and collect data
panic: runtime error: invalid memory address or nil pointer dereference

[lx] Stop: Execution failed. Fix your Go code first.
[lx] Forced termination detected. Restoring source code...

```

Your `main.go` remains exactly as you wrote it, free of any internal `lx.Spy` injections, ready for you to fix the bug.

---


# Usage

`lx` operates on a simple premise: you define the boundaries, and the AI fills the implementation based on real runtime data.

## Workflow

You only need to define two things to command the AI:

1. **Signature:** A standard Go function declaration. Just write normal Go code. `lx` natively understands your function name, parameters (if any), and return types (if any).
2. **Intent:** Your instruction using `lx.Gen(...)` inside the function body.

### Step 1: Write the Contract

```go
// test.go
package main

import (
	"fmt"

	"github.com/chebread/lx"
)

func main() {
	// You own the flow.
	greeting := LX_Greeter("Gopher", true)
	LX_PrintBanner(greeting)
}

// Case A: Function with Return Value
func LX_Greeter(name string, isMorning bool) string {
	// Intent: describe the logic. (captured at runtime)
	lx.Gen(fmt.Sprintf("Greet %s politely. Is it morning? %v", name, isMorning))

	// Output: dummy return that matches the real output shape.
	return "Dummy Greeting String"
}

// Case B: Void Function (Side Effects)
func LX_PrintBanner(message string) {
	// Intent: command the AI. 
	// No dummy return needed for void functions.
	lx.Gen("Print the message inside a stylized ASCII banner to stdout.")
}

```

### Step 2: Run the CLI Tool

`lx` only generates code for functions that are actually reached during execution. If a function is never called, `lx` assumes it is dead code and skips it entirely.

```bash
lx [FLAGS...] [PATH]

```

* **FLAGS**
  * `-version`: Print the current version of `lx`
  * `-timeout=5m`: stop capture if your program doesn't exit (e.g., `9s`, `1m2s`, `2m`)
  * `-show-stdout=true`: show your program’s stdout (trace lines excluded)
  * `-max-prompt, -max-context, -max-output`: bound what gets sent to the LLM

* **PATH**: Can be `.` (project root), a relative path, or an absolute path. Defaults to `.`.

### Step 3: Destructive Transformation

After analyzing the runtime data, `lx` performs a destructive transformation **only** on the functions containing `lx.Gen`. It replaces the entire body of those specific functions with deterministic, implemented logic. The rest of your project remains completely untouched.

```go
func LX_Greeter(name string, isMorning bool) string {
	// lx-prompt: Greet Gopher politely. Is it morning? true
	// lx-dep: fmt

	if isMorning {
		return fmt.Sprintf("Good morning, %s! Hope you have a productive day.", name)
	}
	return fmt.Sprintf("Good evening, %s. Time to rest.", name)
}

func LX_PrintBanner(message string) {
	// lx-prompt: Print the message inside a stylized ASCII banner to stdout.
	// lx-dep: fmt

	fmt.Println("****************************************")
	fmt.Printf("* %s *\n", message)
	fmt.Println("****************************************")
}

```

---

## One Function, One Task
LLM Functionalization means mapping one function to one AI task.

lx replaces the entire body of any function containing lx.Gen.
Do not mix orchestration logic and AI intent in the same function.

### Bad example
The AI will overwrite your main logic, and MyWorker() will never be called.

```go
func main() {
    lx.Gen("Print hello") // <--- This takes over the whole function!
    MyWorker()            // <--- This will be DELETED.
}
```

### Good example
Keep your control flow (Orchestrator) separate from AI tasks (Functional Unit).

```go
func main() {
    // Orchestrator: You own the flow.
    PrintHello()
    MyWorker()
}

func PrintHello() {
    // Functional Unit: AI owns the implementation.
    lx.Gen("Print hello")
}
```

---

## Dependency Management

`lx` strictly adheres to the Single Responsibility Principle (SRP). Its job is to synthesize logic, not to manage your infrastructure. Unlike other AI tools that might silently modify your `go.mod` or install unvetted packages, `lx` ensures you remain the final gatekeeper for every dependency added to your project.

### How it works

If the AI determines that a task requires an external library (e.g., `google/uuid` or `stretchr/testify`), it will:

1. Implement the logic using that library.
2. Flag the dependency explicitly with an `// lx-dep:` comment inside the function.
3. Report the requirement to your terminal after the generation phase.

### Example: Generating a UUID

#### 1. Your Intent

You define the contract and the need for a unique identifier.

```go
func LX_GenerateID() string {
    // Intent: Generate a version 4 UUID.
    lx.Gen("Generate a unique V4 UUID string.")

    return "dummy-uuid"
}

```

#### 2. The `lx` Result

`lx` generates the code but flags the new dependency instead of installing it.

```go
func LX_GenerateID() string {
    // lx-prompt: Generate a unique V4 UUID string.
    // lx-dep: github.com/google/uuid

    // Note: You must manually 'go get github.com/google/uuid' 
    // and add it to your imports.
    return uuid.New().String()
}

```

#### 3. Your Action

When you see the `// lx-dep` comment or the terminal report, you simply run:

```bash
go get github.com/google/uuid

```

### Why this matters

* Supply Chain Security: You prevent "hallucinated" or malicious packages from entering your codebase without a manual review.
* Clean `go.mod`: No zombie dependencies or unexpected version bumps.
* Reviewable Changes: Every new dependency is explicitly tied to the function that requires it, making PR reviews straightforward.

---

## Capture Mode (`LX_MODE=capture`)

When you run `lx .`, the tool doesn't just look at your code—it executes it. To make sure this "observation run" doesn't do anything it shouldn't (or to make sure it sees things it usually wouldn't), `lx` injects an environment variable: `LX_MODE=capture`.

Think of this as a Simulation Mode. The code inside this `if` block triggers ONLY when `lx` is running.

### Why use this?

* The Safety Switch: Prevent the AI from actually wiping your production database or sending 10,000 emails while it's just trying to learn your function signatures.
* The Discovery Trigger: If your program requires specific CLI flags to run certain functions, the AI might miss them during a default `lx .` run. You can use this block to force-call those functions so the AI can "see" them and write the code for you.

### Example

In this example, we use the capture block to both silence a dangerous operation and trigger functions that the AI would otherwise skip.

```go
func main() {
    flag.Parse()

    if os.Getenv("LX_MODE") == "capture" {
        fmt.Println("[lx] Simulation mode active. Triggering discovery...")
        
        LX_FindFiles("dummy-search", nil, nil, "all")
        LX_SearchTextInFiles("dummy-query", nil, nil, "text")
        
        os.Exit(0) 
    }

    if *deleteEverything {
        RealDatabaseDelete() 
    }
}

```

### Key Logic Check

* `if == "capture"`: "I am currently being watched by `lx`. Do special simulation stuff now."
* The Result: Your production code stays safe, and the AI gets all the runtime data it needs to build your functions.

---

## Dependency Isolation & Cross-Platform Capture (`-tags`)

`lx` executes your code to capture runtime data. However, if your code depends on hardware-specific packages (like `machine` in TinyGo), platform-specific APIs (Windows syscalls), or heavy external libraries not present on your local machine, the capture phase will fail to compile.

You can solve this using Go's native Build Tags to "mock" these dependencies.

### Step 1: Split your logic

Create a mock version of your platform-dependent code.

`hardware_real.go` (Targeting the actual device)

```go
//go:build !lx_mock

package main
import "machine" // TinyGo specific package

func InitHardware() {
	machine.GP15.Configure(machine.PinConfig{Mode: machine.PinInputPullup})
}

```

`hardware_mock.go` (Targeting your local PC for lx capture)

```go
//go:build lx_mock

package main

func InitHardware() {
	// No-op or log for local capture
}

```

### Step 2: Run lx with tags

Tell `lx` to include your mock implementation during the capture run. This allows `lx` to bypass non-existent packages on your Mac/Linux and successfully capture the data flow.

```bash
# You can use any tag name (e.g., mock, dev, capture)
lx -tags lx_mock .

```

### Why this is powerful:

* Universal Capture: Generate code for a $2 Pico or a Windows Server while working on a MacBook.
* Environment Independence: Run `lx` without setting up complex databases or CGO dependencies by mocking them during the capture phase.
* Pure Logic Focus: By isolating "noisy" infrastructure, the AI focuses 100% on synthesizing your core business logic.

---

## Which Isolation to Use: `-tags` vs `LX_MODE`

To capture runtime data, `lx` must first compile and then execute your code. Depending on where the roadblock is, you need a different isolation strategy.

| Strategy | Layer | Problem Solved | Mechanism |
| --- | --- | --- | --- |
| `-tags` | Compile-time | "My code won't even build on this machine." | `//go:build` tags |
| `LX_MODE` | Run-time | "My code builds, but it's dangerous/silent when run." | `os.Getenv("LX_MODE")` |

### 1. Scenario A: Compiling the Un-compilable (`-tags`)

Use this when your code depends on hardware-specific packages (e.g., TinyGo's `machine`) or OS-specific APIs (e.g., `unix` or `windows` syscalls) that aren't available on your local development machine.

The Problem: `lx` runs `go run .` to see your code in action. If it can't find `machine` on your MacBook, it crashes before it even starts.

The Solution: Create a mock file for the local environment.

```go
// sensor_mock.go
//go:build lx_mock

package main

func ReadHardwareSensor() int {
    // Return a dummy value so lx can see the "shape" of the data
    return 42 
}

```

How to run:

```bash
lx -tags lx_mock .

```

### 2. Scenario B: Controlling the Execution Flow (`LX_MODE`)

Use this when your code compiles perfectly, but you want to change its behavior while `lx` is "watching" it. This is crucial for safety and ensuring all functions are reached.

The Problem: You have a function `LX_DeleteUser()` that you want AI to implement, but you don't want to actually delete users during the capture phase. Or, you have a function that only runs if a specific CLI flag is passed.

The Solution: Use the `LX_MODE=capture` environment variable.

```go
// main.go
func main() {
    if os.Getenv("LX_MODE") == "capture" {
        // 1. Safety: Skip destructive operations
        fmt.Println("[lx] Skipping DB wipe for safety.")
        
        // 2. Discovery: Force-trigger functions for AI to see
        LX_ProcessPayment(100.0, "USD") 
        
        os.Exit(0) // Exit early after triggering discovery
    }

    // Real production logic
    RealDangerousWipe()
}

```

### 3. Combining Both: The Pro Strategy

In professional projects, you often need both. You mock the low-level hardware drivers so the code compiles, and you use the capture mode to feed the AI realistic "simulated" data.

```go
func main() {
    // 1. Works on Mac because of '-tags lx_mock'
    InitHardware() 

    if os.Getenv("LX_MODE") == "capture" {
        // 2. Provides dummy data for the AI to learn from
        input := "Test User"
        fmt.Println(LX_Greeter(input, true))
        os.Exit(0)
    }
}

```

> [!TIP]
> Rule of Thumb:
> * If `go build` fails on your machine  Use `-tags`.
> * If `go run` works but you're afraid of the side effects  Use `LX_MODE`.

----

## Incremental Refinement

In the Vibe Coding paradigm, if you need to modify a 100-line function, you highlight the whole block, type *"also ignore hidden files"*, and pray the AI doesn't break the rest of the logic.

`lx` fundamentally rejects this. Wiping a working function and replacing it entirely with a new `lx.Gen` prompt destroys your control and wastes the deterministic logic you already secured. With `lx`, reading code is mandatory. You are the architect.

When you need to modify an AI-generated function, you don't rewrite; you refactor. You elevate the existing function to an Orchestrator, and extract the new requirement into a focused Functional Unit.

### Example

You used `lx` to generate `LX_ListDirectory`. It successfully reads the directory and returns files. Later, you realize you need a new feature: ignore hidden files (starting with `.`).

### The Vibe Coding Way (Do not do this)

Deleting the entire working function and starting over forces the AI to reinvent the wheel. It might change your variables, alter the traversal logic, or introduce new bugs.

```go
// BAD: Wiping perfectly good code just to add a small feature.
func LX_ListDirectory(path string) []string {
    // lx.Gen("List files but this time ignore hidden files starting with a dot")
    return nil
}

```

### The `lx` Way: Extract & Conquer

Instead of destroying the function, take ownership of it. Read the code, find the exact insertion point, and delegate only the new logic to a new `lx.Gen` target.

#### Step 1: Claim the Orchestrator

Insert a call to a new function right where the filtering should happen.

```go
func LX_ListDirectory(path string) []string {
    var files []string
    entries, _ := os.ReadDir(path) // AI wrote this originally, and it works.
    
    for _, entry := range entries {
        // You manually insert this new branching logic.
        if LX_ShouldKeep(entry.Name()) {
            files = append(files, entry.Name())
        }
    }
    return files
}

```

#### Step 2: Define the new Functional Unit

Create the new function, pass in simple primitive types (like `string`), and let `lx` handle the implementation details.

```go
// AI takes over ONLY this tiny, isolated decision.
func LX_ShouldKeep(name string) bool {
    lx.Gen("Return true if the name does NOT start with a dot (.). Otherwise, false.")
    
    return true // Dummy return
}

```

### Why this matters

1. Zero Regression: Your directory reading logic remains untouched and unbreakable.
2. Readability: Your code naturally breaks down into smaller, highly readable chunks.
3. Absolute Sovereignty: The AI didn't decide where to filter the files; you did. The AI only implemented how to check for a dot.

---

# Installation & Config

To use lx, you need both the CLI tool and the Go library. Here is the straightforward setup for any Gopher.

## 1. Install the CLI

`lx` is deployed via Homebrew for macOS.

```bash
brew tap chebread/lx
brew install lx

```

> [!NOTE]
> Currently, only macOS (Intel/Apple Silicon) is supported.

## 2. Add the Library to your Project

Add the dependency to your project to use `lx.Gen()` in your code.

```bash
go get github.com/chebread/lx

```

## 3. Configuration

Create an `lx-config.yaml` file in your home directory (`~/`) or project root.
`lx` supports two modes: Direct API and Universal Command.

### Option A: Direct API (Google Gemini)

The simplest setup if you have a Google API Key.

```yaml
provider: "gemini"
api_key: "YOUR_API_KEY"
model: "gemini-2.0-flash"

```

### Option B: Universal CLI (Gemini, Claude, Ollama, etc.)

`lx` can wrap any CLI tool installed on your machine.
Use the `command` provider and define the argument template. `lx` will automatically substitute `{{prompt}}` and `{{model}}` at runtime.

#### 1. Google Gemini CLI (Zero Cost / No API Key in Config)

```yaml
provider: "command"
bin_path: "/usr/local/bin/gemini" # Check with `which gemini`
model: "gemini-2.0-flash"
args:
  - "-p"
  - "{{prompt}}"
  - "-m"
  - "{{model}}"
  - "-o"
  - "text"

```

#### 2. Claude Code (Anthropic)

```yaml
provider: "command"
bin_path: "/usr/local/bin/claude"
model: "claude-3-7-sonnet"
args:
  - "-p"
  - "{{prompt}}"
  - "--model"
  - "{{model}}"

```

#### 3. Ollama (Local / Offline / Free)

Run Llama 3 or DeepSeek locally without internet.

```yaml
provider: "command"
bin_path: "/usr/local/bin/ollama"
model: "llama3"
args:
  - "run"
  - "{{model}}"
  - "{{prompt}}"

```


## 4. Hierarchical Configuration

`lx` uses a hierarchical configuration system. If a configuration file exists in both locations, the Local configuration takes strict priority. This allows you to set a global default (e.g., a cloud API) while keeping specific projects completely offline or on a different model.

### 1. Global Config

Located at `~/lx-config.yaml` (or your OS equivalent), this defines your default preferences across all projects. For example, setting Google Gemini as your standard fallback:

```yaml
# ~/lx-config.yaml
provider: "gemini"
api_key: "YOUR_API_KEY"
model: "gemini-2.0-flash"

```

### 2. Local Config

Located in your project root as `./lx-config.yaml`. This allows you to tailor `lx` to the specific needs of a workspace. For example, forcing a specific project to use a local Ollama model without sending data to the cloud:

```yaml
# ./my-project/lx-config.yaml
provider: "command"
bin_path: "/usr/local/bin/ollama"
model: "llama3"
# 'args' is a flexible array. 'lx' automatically substitutes 
# {{model}} and {{prompt}} at runtime.
args:
  - "run"
  - "{{model}}"
  - "{{prompt}}"

```

### 3. Effective Result & Testing

When running `lx` inside `my-project`, the tool detects the local file and completely overrides the global settings.

You can test this hierarchy directly in your terminal. `lx` always prints which configuration file it loaded and the active provider/model before executing.

```bash
$ cd my-project
$ lx .

[lx] Start running...
[lx] Config: ./lx-config.yaml [Local]
[lx] Provider: [command] / Model: [llama3]
[lx] Converting code...

```

If you step outside `my-project` and run `lx` in a directory without a local config, it will automatically fall back:

```bash
$ cd /some/other/path
$ lx .

[lx] Start running...
[lx] Config: ~/lx-config.yaml [Global]
[lx] Provider: [gemini] / Model: [gemini-2.0-flash]
[lx] Converting code...

```

---

# License
This project is licensed under the [AGPL-3.0 License](./LICENSE).
