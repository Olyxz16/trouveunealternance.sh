package scraper

import (
	"bytes"
	"net/url"
	"strings"

	"github.com/go-shiori/go-readability"
	"github.com/markusmobius/go-trafilatura"
	"golang.org/x/net/html"
)

type Extractor struct{}

func NewExtractor() *Extractor {
	return &Extractor{}
}

func (e *Extractor) Extract(htmlStr, rawURL string) (string, error) {
	// 1. Preprocess
	htmlStr = preprocess(htmlStr, rawURL)

	// 2. Trafilatura
	res, err := trafilatura.Extract(strings.NewReader(htmlStr), trafilatura.Options{
		ExcludeComments: true,
		ExcludeTables:   false,
	})
	if err == nil && isGoodQuality(res.ContentText) {
		var buf bytes.Buffer
		if err := html.Render(&buf, res.ContentNode); err == nil {
			md, _ := ToMarkdown(buf.String())
			// If it's too short, append raw body to ensure we have links
			if len(md) < 2000 {
				rawMD, _ := ToMarkdown(htmlStr)
				md = md + "\n\n--- FULL PAGE LINKS ---\n" + rawMD
			}
			return md, nil
		}
	}

	// 3. Readability
	parsedURL, _ := url.Parse(rawURL)
	article, err := readability.FromReader(strings.NewReader(htmlStr), parsedURL)
	if err == nil && isGoodQuality(article.Content) {
		md, _ := ToMarkdown(article.Content)
		if len(md) < 2000 {
			rawMD, _ := ToMarkdown(htmlStr)
			md = md + "\n\n--- FULL PAGE LINKS ---\n" + rawMD
		}
		return md, nil
	}

	// 4. Raw body (last resort)
	return ToMarkdown(htmlStr)
}

func isGoodQuality(content string) bool {
	if len(content) < 500 {
		return false
	}
	
	lowContent := strings.ToLower(content)
	errorSignals := []string{"captcha", "blocked", "access denied", "robot check", "please wait...", "checking your browser"}
	for _, sig := range errorSignals {
		if strings.Contains(lowContent, sig) {
			return false
		}
	}

	return true
}
