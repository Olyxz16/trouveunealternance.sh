package llm

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

type GeminiCLIProvider struct {
	BinaryPath string
}

func NewGeminiCLIProvider(path string) *GeminiCLIProvider {
	if path == "" {
		path = "gemini"
	}
	return &GeminiCLIProvider{BinaryPath: path}
}

func (p *GeminiCLIProvider) Name() string {
	return "gemini_cli"
}

func (p *GeminiCLIProvider) ProviderName() string {
	return "gemini_cli"
}

func (p *GeminiCLIProvider) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	fullPrompt := req.System + "\n\n" + req.User
	
	// -p: headless prompt mode
	// --raw-output: avoid TUI/interactive overhead
	cmd := exec.CommandContext(ctx, p.BinaryPath, "-p", fullPrompt, "--raw-output")
	
	var out bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	
	if err := cmd.Run(); err != nil {
		return CompletionResponse{}, fmt.Errorf("gemini cli failed: %w (stderr: %s)", err, stderr.String())
	}
	
	content := strings.TrimSpace(out.String())
	
	// Estimated tokens: chars / 4
	promptTokens := len(fullPrompt) / 4
	completionTokens := len(content) / 4
	
	return CompletionResponse{
		Content:          content,
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		EstimatedCost:    true,
	}, nil
}
