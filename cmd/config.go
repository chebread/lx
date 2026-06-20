package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type options struct {
	targetDir      string
	timeout        time.Duration
	showStdout     bool
	maxPromptChars int
	maxBodyChars   int
	maxOutputBytes int
	tags           string
}

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

func runConfigWizard(configPath string, isLocal bool) error {
	fmt.Printf("[lx] Starting configuration wizard for %s config.\n", map[bool]string{true: "local", false: "global"}[isLocal])
	fmt.Printf("[lx] Config file path: %s\n\n", configPath)

	var cfg Config
	// Default starting settings if file doesn't exist
	cfg.Provider = "gemini"
	cfg.Model = "gemini-2.0-flash"

	// If file exists, load it to pre-populate defaults
	if _, err := os.Stat(configPath); err == nil {
		data, err := os.ReadFile(configPath)
		if err == nil {
			_ = yaml.Unmarshal(data, &cfg)
		}
	}

	reader := bufio.NewReader(os.Stdin)

	// Helper to prompt and get clean input
	prompt := func(msg string, defaultVal string) string {
		if defaultVal != "" {
			fmt.Printf("%s [%s]: ", msg, defaultVal)
		} else {
			fmt.Printf("%s: ", msg)
		}
		input, err := reader.ReadString('\n')
		if err != nil {
			return defaultVal
		}
		input = strings.TrimSpace(input)
		if input == "" {
			return defaultVal
		}
		return input
	}

	// 1. Select provider
	var provider string
	defaultProviderChoice := "1"
	if strings.ToLower(cfg.Provider) == "command" {
		defaultProviderChoice = "2"
	}
	for {
		choice := prompt("Select LLM Provider (1: gemini, 2: command)", defaultProviderChoice)
		if choice == "1" || strings.ToLower(choice) == "gemini" {
			provider = "gemini"
			break
		} else if choice == "2" || strings.ToLower(choice) == "command" {
			provider = "command"
			break
		}
		fmt.Println("Invalid option. Please choose 1 or 2.")
	}

	cfg.Provider = provider

	if provider == "gemini" {
		var apiKey string
		for {
			apiKey = prompt("Enter Gemini API Key (required)", cfg.ApiKey)
			if apiKey != "" {
				break
			}
			fmt.Println("API Key cannot be empty. Please enter a valid API Key.")
		}
		cfg.ApiKey = apiKey

		defaultModel := cfg.Model
		if defaultModel == "" || defaultModel == "llama3.1" {
			defaultModel = "gemini-2.0-flash"
		}
		cfg.Model = prompt("Enter Gemini Model", defaultModel)
		cfg.BinPath = ""
		cfg.Args = nil
	} else {
		var binPath string
		for {
			binPath = prompt("Enter Command binary path (bin_path) (required)", cfg.BinPath)
			if binPath != "" {
				break
			}
			fmt.Println("Binary path cannot be empty. Please enter a valid binary path.")
		}
		cfg.BinPath = binPath

		defaultModel := cfg.Model
		if defaultModel == "" || defaultModel == "gemini-2.0-flash" {
			defaultModel = "llama3.1"
		}
		cfg.Model = prompt("Enter Command Model", defaultModel)

		defaultArgsStr := strings.Join(cfg.Args, " ")
		argsStr := prompt("Enter arguments (space-separated, e.g. '-p {{prompt}} -m {{model}}')", defaultArgsStr)
		if argsStr != "" {
			cfg.Args = strings.Fields(argsStr)
		} else {
			cfg.Args = nil
		}
		cfg.ApiKey = ""
	}

	// Ensure the parent directory exists
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	// Write YAML to file
	data, err := yaml.Marshal(&cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	if err := os.WriteFile(configPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	fmt.Printf("\n[lx] Configuration successfully saved to %s\n\n", configPath)
	return nil
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
	if err != nil {
		return nil, "", fmt.Errorf("could not find user home directory: %w", err)
	}

	globalPath := filepath.Join(home, ".config", "lx", "lx-config.yaml")
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

	// If neither exists, trigger interactive setup
	err = runConfigWizard(globalPath, false)
	if err != nil {
		return nil, "", fmt.Errorf("could not create config file: %w", err)
	}

	// Load the newly created config
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
