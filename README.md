# **lx**: Controllable AI for Gophers!

`lx` restores the [**joy of wrangling code**](https://tidyfirst.substack.com/p/augmented-coding-beyond-the-vibes) by letting you define the forest, while AI plants the trees under your intentional command.

---

# Table of Contents
- [Philosophy](#features)
- [Why lx?](#why-lx)
- [Example](#example)
- [Usage](#usage)
- [Configuration](#configuration)
- [Dependency Management](#dependency-management)
- [Installation](#installation)
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
**If it is not under our control, it's not programming anymore.**
That is why I built `lx`.
`lx` puts the hammer back in the Gopher's hand, restoring absolute control.
It allows you to command AI strictly on your terms through a new paradigm: LLM Functionalization.
It all works in your editor, on your machine, under your terms.

---
# Why lx?

## 1. Developer Sovereignty
The function signature is the contract. `lx` treats your function names, parameters, and return types as an immutable contract.
AI cannot change your architecture; it can only fulfill the implementation details you demand.

## 2. Deterministic AI Coding
`lx` doesn't just read your code; it runs it.
Through Trace & Patch, `lx` replaces probabilistic guesswork with runtime facts.
The result is code that aligns with your real inputs and expected outputs.

## 3. Continuous Development
With `lx`, once you write a function’s inputs and outputs, you can keep moving as if the logic is already there.
But it’s not “design-only” — your program still has to compile and run.
You drive the flow and the contract; `lx` uses runtime traces to fill in the function internals.

## 4. Freedom of Editor
VS Code? Vim? Zed? Cursor? It doesn't matter.
`lx` is a CLI tool. It works where you work.

## 5. Smart Generation
`lx` is efficient and safe.
Once a function is implemented, `lx` remembers it.
It will never overwrite your existing logic, unless you explicitly delete the body.
You can run `lx` a thousand times without fear.

## 6. Hierarchical Configuration
`lx` follows strict precedence:
Local config (project) overrides Global config (home).
It adapts to your workspace, not the other way around.

---
# Usage

You only need three things:

1. **Input**: function params (real runtime values)
2. **Intent**: `lx.Gen(...)` (your instruction)
3. **Output**: return type + dummy return (the expected shape)

`lx` operates strictly at function scope.
It will rewrite the function body, but it will not change your function signature.

> Note: `lx` executes your program during capture. Any runtime side effects in your code will happen.

## Step 1: Write the contract
```go
// test.go
package main

import (
	"fmt"

	"github.com/chebread/lx"
)

func main() {
	// You own the flow.
	fmt.Println(LX_Greeter("Gopher", true))
}

func LX_Greeter(name string, isMorning bool) string {
	// Intent: describe the logic. (captured at runtime)
	lx.Gen(fmt.Sprintf("Greet %s politely. Is it morning? %v", name, isMorning))

	// Output: dummy return that matches the real output shape.
	return "Dummy Greeting String"
}
```

## Step 2: Run lx

Your code **must compile and run**.
`lx` will run your program once to **capture runtime evidence** (real prompt inputs + sample outputs).

```bash
lx [flags...] [PATH]
```

* `PATH` can be `.` (project root), a relative path, or an absolute path.
* If omitted, `lx` defaults to the current directory (`.`).

### Capture Mode (`LX_MODE=capture`)

During the capture run, `lx` sets:

* `LX_MODE=capture`

This is intentional: it lets you **explicitly control side effects** during capture.
If your program does anything risky (network calls, DB writes, file operations), guard it:

```go
if os.Getenv("LX_MODE") == "capture" {
    // capture run: keep it safe and quiet
    // (no network calls, no DB writes, no destructive operations)
}
```

### What `lx [PATH]` does

1. Injects spies into target functions (wraps returns with `lx.Spy(...)`)
2. Runs `go run .` **inside the target directory** to capture real inputs and expected outputs
3. Reverts the injected spies immediately (restores your source files)
4. Generates and patches the function bodies based on the captured evidence

### Useful flags (optional)

* `-timeout=2m`: stop capture if your program doesn’t exit
* `-show-stdout=true`: show your program’s stdout (trace lines excluded)
* `-max-prompt, -max-context, -max-output`: bound what gets sent to the LLM


## Step 3: Review the result

```go
func LX_Greeter(name string, isMorning bool) string {
	// lx-prompt: Greet Gopher politely. Is it morning? true
	// lx-dep: fmt

	if isMorning {
		return fmt.Sprintf("Good morning, %s! Hope you have a productive day.", name)
	}
	return fmt.Sprintf("Good evening, %s. Time to rest.", name)
}
```

---

# Example

## Test code
```go
package main

import (
	"fmt"

	"github.com/chebread/lx"
)

func main() {
	var message = "happ"
	fmt.Println(LX_Func1(message + "y"))
	fmt.Println("-----------------------")
	var isTrue = LX_Func2("홍")
	if isTrue {
		fmt.Println("Yes")
	} else {
		fmt.Println("No")
	}
}

func LX_Func1(a string) string {
	lx.Gen(fmt.Sprintf("I am %s", a))
	var x = "bar"
	return x
}

func LX_Func2(k string) bool {
	lx.Gen(fmt.Sprintf("Determine if current system seconds are odd (true) or even (false). Use time package. Input %s name is mandatory per contract but unused in logic.", k+"길동"))
	var foo bool = false
	return !foo
}
```


## Generated code
```go
package main

import (
	"fmt"
	"time"
)

func main() {
	var message = "happ"
	fmt.Println(LX_Func1(message + "y"))

	fmt.Println("-----------------------")

	var isTrue = LX_Func2("홍길동")
	if isTrue {
		fmt.Println("Yes")
	} else {
		fmt.Println("No")
	}
}

func LX_Func1(a string) string {
	// lx-prompt: I am happy

	// lx-dep: fmt
	x := fmt.Sprintf("I am %s", a)
	return x
}

func LX_Func2(k string) bool {
	// lx-prompt: Determine if current system seconds are odd (true) or even (false). Use time package. Input 홍길동 name is mandatory per contract but unused in logic.

	// lx-dep: time
	return time.Now().Second()%2 != 0
}
```

## Terminal results

```bash
$ lx --show-stdout .

[lx] Start running...
[lx] Config: ~/lx-config.yaml [Global]
[lx] Provider: [gemini] / Model: [gemini-3-flash-preview]
[lx] Converting code
[lx] Run the program and collect data
	[INPUT] LX_Func1: lx-prompt: I am happy
	[OUTPUT] LX_Func1: I am happy
	[capture stdout] I am happy
	[capture stdout] -----------------------
	[capture stdout] No
[lx] Restore the source code
[lx] Analyze the collected data and generating code
	[Data] LX_Func1: Input="lx-prompt: I am happy", Output=Confirmed
[lx] [/Users/ihaneum/work/lx/test/test.go -> LX_Func1] Generate code
[lx] [/Users/ihaneum/work/lx/test/test.go -> LX_Func1] code generation failed
[lx] Error: You have exceeded your API call quota. Please try again later or check your payment information.
[lx] All tasks completed
```

```bash
$ go run .
I am happy
-----------------------
Yes
```

---

# Configuration

Create an `lx-config.yaml` file in your home directory (`~/`) or project root.

```yaml
provider: "gemini"
api_key: "YOUR_API_KEY"
model: "foo_bar" 
```

***Currently, only Google Gemini is supported.***

---

# Dependency Management

`lx` respects the Single Responsibility Principle(SRP). It generates code, but it doesn't silently install packages.
If the AI uses a new library (e.g., `github.com/google/uuid`), `lx` will:

1. Add `// lx-dep: ...` comments in the code.
2. Report any issues to the terminal.

---

# Installation

To use lx, you need both the CLI tool and the Go library. Here is the straightforward setup for any Gopher.

## 1. Install the CLI
`lx` is deployed via Homebrew for macOS.

```bash
brew tap chebread/lx
brew install lx

```

***Currently, only macOS (Intel/Apple Silicon) is supported.***

## 2. Add the Library to your Project
Add the dependency to your project to use `lx.Gen()` in your code.

```bash
go get github.com/chebread/lx
```

# License
This project is licensed under the **AGPL-3.0 License**.
