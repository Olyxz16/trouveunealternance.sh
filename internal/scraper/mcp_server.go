package scraper

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"time"

	mcp "github.com/metoro-io/mcp-golang"
	"github.com/metoro-io/mcp-golang/transport/stdio"
)

type MCPServer struct {
	port int
}

func NewMCPServer(port int) *MCPServer {
	return &MCPServer{port: port}
}

func (s *MCPServer) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/fetch", s.handleFetch)

	addr := fmt.Sprintf(":%d", s.port)
	log.Printf("MCP Blueprint Bridge starting on %s", addr)
	return http.ListenAndServe(addr, mux)
}

func (s *MCPServer) handleFetch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		URL string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	log.Printf("MCP Blueprint Fetching: %s", req.URL)

	html, err := s.fetchWithBlueprint(req.URL)
	if err != nil {
		log.Printf("MCP Blueprint error: %v", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"html": html,
	})
}

func (s *MCPServer) fetchWithBlueprint(targetURL string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// 1. Start the Blueprint MCP server via npx
	cmd := exec.CommandContext(ctx, "npx", "@railsblueprint/blueprint-mcp@latest")
	
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return "", err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("failed to start npx: %w", err)
	}
	defer cmd.Process.Kill()

	// 2. Setup MCP client over stdio
	// Note: We swap stdout/stdin because the server's stdout is our input
	clientTransport := stdio.NewStdioServerTransportWithIO(stdout, stdin)
	mcpClient := mcp.NewClient(clientTransport)

	// Initialize
	_, err = mcpClient.Initialize(ctx)
	if err != nil {
		return "", fmt.Errorf("mcp init failed: %w", err)
	}

	// 3. Call tools: enable (to connect to browser)
	_, err = mcpClient.CallTool(ctx, "enable", map[string]interface{}{})
	if err != nil {
		log.Printf("Warning: enable tool failed (might already be enabled): %v", err)
	}

	// 4. Call tools: browser_navigate
	_, err = mcpClient.CallTool(ctx, "browser_navigate", map[string]interface{}{
		"url": targetURL,
	})
	if err != nil {
		return "", fmt.Errorf("browser_navigate failed: %w", err)
	}

	// Wait a bit for JS
	time.Sleep(2 * time.Second)

	// 5. Call tools: browser_extract_content
	res, err := mcpClient.CallTool(ctx, "browser_extract_content", map[string]interface{}{
		"mode": "full",
	})
	if err != nil {
		return "", fmt.Errorf("browser_extract_content failed: %w", err)
	}

	// Parse the tool response
	if len(res.Content) == 0 {
		return "", fmt.Errorf("empty content from blueprint")
	}

	// Blueprint usually returns text in the first content block
	if res.Content[0].TextContent != nil {
		return res.Content[0].TextContent.Text, nil
	}

	return "", fmt.Errorf("unexpected content type from blueprint")
}
