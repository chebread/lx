package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"google.golang.org/genai"
)

type LLM interface {
	Generate(ctx context.Context, model string, prompt string) (string, error)
}

type commandLLM struct {
	binPath string
	args    []string
}

type geminiLLM struct {
	client *genai.Client
}

func newLLM(cfg *Config) (LLM, error) {
	if cfg == nil {
		return nil, errors.New("nil config")
	}

	if strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("empty model")
	}

	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	if provider == "" {
		provider = "gemini"
	}

	switch provider {
	case "gemini":
		if strings.TrimSpace(cfg.ApiKey) == "" {
			return nil, errors.New("empty api_key")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		client, err := genai.NewClient(ctx, &genai.ClientConfig{APIKey: cfg.ApiKey})
		if err != nil {
			return nil, err
		}
		return &geminiLLM{client: client}, nil

	case "command":
		if strings.TrimSpace(cfg.BinPath) == "" {
			return nil, errors.New("empty bin_path (required for command provider)")
		}

		return &commandLLM{
			binPath: cfg.BinPath,
			args:    cfg.Args,
		}, nil

	default:
		return nil, fmt.Errorf("unsupported provider: %s", cfg.Provider)
	}
}

func (g *geminiLLM) Generate(ctx context.Context, model string, prompt string) (string, error) {
	resp, err := g.client.Models.GenerateContent(ctx, model, genai.Text(prompt), nil)
	if err != nil {
		return "", err
	}
	return resp.Text(), nil
}

func (c *commandLLM) Generate(ctx context.Context, model string, prompt string) (string, error) {
	var finalArgs []string

	if len(c.args) == 0 {
		finalArgs = []string{"-p", prompt, "-m", model, "-o", "text"}
	} else {
		for _, arg := range c.args {
			replaced := strings.ReplaceAll(arg, "{{prompt}}", prompt)
			replaced = strings.ReplaceAll(replaced, "{{model}}", model)
			finalArgs = append(finalArgs, replaced)
		}
	}

	cmd := exec.CommandContext(ctx, c.binPath, finalArgs...)

	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	cmd.Cancel = func() error {
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("timeout reached (%s): process group killed", ctx.Err())
		}
		return "", fmt.Errorf("command execution failed: %v\nStderr: %s", err, stderr.String())
	}

	return out.String(), nil
}

func diagnoseLLMError(err error) string {
	msg := err.Error()

	switch {
	case strings.Contains(msg, "timeout reached"):
		return fmt.Sprintf("TIMEOUT: The operation exceeded the time limit. (%s)", msg)

	case strings.Contains(msg, "API_KEY_INVALID"):
		return "The API key is incorrect. Please double-check the api_key in 'lx-config.yaml'."
	case strings.Contains(msg, "quota"):
		return "You have exceeded your API call quota. Please try again later or check your payment information."
	case strings.Contains(msg, "model not found"):
		return "The specified model could not be found. Please verify that the model name is correct."
	case strings.Contains(msg, "safety"):
		return "Your response has been blocked by security policy. Please edit the prompt."
	case strings.Contains(msg, "connection") || strings.Contains(msg, "timeout"):
		return "The network connection is unstable. Please check your Internet connection."

	default:
		return fmt.Sprintf("An unknown error has occurred: %v", err)
	}
}
