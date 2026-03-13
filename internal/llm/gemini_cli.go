package llm

import (
	"bytes"
	"context"
	"os/exec"
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

func (p *GeminiCLIProvider) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	// Gemini CLI typically takes the prompt as stdin or argument.
	// Based on PLAN.md: "Pipes prompt to stdin of the `gemini` binary, captures stdout."
	
	fullPrompt := req.System + "\n\n" + req.User
	
	cmd := exec.CommandContext(ctx, p.BinaryPath, "ask", "--quiet")
	cmd.Stdin = bytes.NewBufferString(fullPrompt)
	
	var out bytes.Buffer
	cmd.Stdout = &out
	
	if err := cmd.Run(); err != nil {
		return CompletionResponse{}, err
	}
	
	content := out.String()
	
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
