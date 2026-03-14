package scraper

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type MCPFetcher struct {
	host   string
	client *http.Client
}

func NewMCPFetcher(host string) *MCPFetcher {
	return &MCPFetcher{
		host: host,
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

func (f *MCPFetcher) Name() string {
	return "mcp"
}

func (f *MCPFetcher) Fetch(ctx context.Context, url string) (string, error) {
	payload := map[string]string{
		"url": url,
	}
	jsonData, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", f.host+"/fetch", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := f.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("MCP fetch failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("MCP error: %d %s", resp.StatusCode, string(body))
	}

	var result struct {
		HTML string `json:"html"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to decode MCP response: %w", err)
	}

	return result.HTML, nil
}
