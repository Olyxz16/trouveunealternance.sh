package scraper

import (
	"context"
)

type FetchResult struct {
	ContentMD string
	Method    string  // "http" | "mcp" | "cache"
	Quality   float64 // 0.0–1.0
}

type Fetcher interface {
	Fetch(ctx context.Context, url string) (string, error) // returns raw HTML
	Name() string
}
