package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"
)

var version = "dev"

func main() {
	var startTime = time.Now()

	var (
		showVersion bool
		opts        options
		initLocal   bool
		initGlobal  bool
	)

	flag.BoolVar(&showVersion, "version", false, "Print version")
	flag.BoolVar(&initLocal, "init-local", false, "Initialize/modify local config (lx-config.yaml)")
	flag.BoolVar(&initGlobal, "init-global", false, "Initialize/modify global config (~/.config/lx/lx-config.yaml)")
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

	if initLocal {
		localPath := "lx-config.yaml"
		err := runConfigWizard(localPath, true)
		if err != nil {
			log.Fatalf("[lx] Config Init Error: %v", err)
		}
		return
	}

	if initGlobal {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Fatalf("[lx] Could not find user home directory: %v", err)
		}
		globalPath := filepath.Join(home, ".config", "lx", "lx-config.yaml")
		err = runConfigWizard(globalPath, false)
		if err != nil {
			log.Fatalf("[lx] Config Init Error: %v", err)
		}
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

	var elapsed = time.Since(startTime)
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
