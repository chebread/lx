package main

import (
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
