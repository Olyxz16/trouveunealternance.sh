package errors

import (
	"fmt"
)

// JobHunterError is the base interface for all JobHunter errors
type JobHunterError interface {
	error
}

// BaseError is a helper struct for common error fields
type BaseError struct {
	Message string
}

func (e *BaseError) Error() string {
	return e.Message
}

// Scraping Errors
type ScrapingError struct {
	BaseError
}

type JinaError struct {
	ScrapingError
	URL             string
	StatusCode      int
	ResponsePreview string
}

func NewJinaError(url string, statusCode int, responsePreview string) *JinaError {
	preview := responsePreview
	if len(preview) > 100 {
		preview = preview[:100]
	}
	return &JinaError{
		ScrapingError: ScrapingError{BaseError{Message: fmt.Sprintf("Jina failed for %s (status %d): %s", url, statusCode, preview)}},
		URL:             url,
		StatusCode:      statusCode,
		ResponsePreview: responsePreview,
	}
}

type MCPError struct {
	ScrapingError
	URL    string
	Reason string
}

func NewMCPError(url string, reason string) *MCPError {
	return &MCPError{
		ScrapingError: ScrapingError{BaseError{Message: fmt.Sprintf("MCP failed for %s: %s", url, reason)}},
		URL:             url,
		Reason:          reason,
	}
}

type EmptyContentError struct {
	ScrapingError
	URL    string
	Method string
}

func NewEmptyContentError(url string, method string) *EmptyContentError {
	return &EmptyContentError{
		ScrapingError: ScrapingError{BaseError{Message: fmt.Sprintf("No content found for %s via %s", url, method)}},
		URL:             url,
		Method:          method,
	}
}

// LLM Errors
type LLMError struct {
	BaseError
}

type RateLimitError struct {
	LLMError
	RetryAfter float64
	Model      string
}

func NewRateLimitError(retryAfter float64, model string) *RateLimitError {
	return &RateLimitError{
		LLMError:   LLMError{BaseError{Message: fmt.Sprintf("Rate limit hit for %s. Retry after %.2fs", model, retryAfter)}},
		RetryAfter: retryAfter,
		Model:      model,
	}
}

type ParseError struct {
	LLMError
	RawResponse    string
	ExpectedSchema string
}

func NewParseError(rawResponse string, expectedSchema string) *ParseError {
	return &ParseError{
		LLMError:       LLMError{BaseError{Message: fmt.Sprintf("Failed to parse LLM response into schema %s", expectedSchema)}},
		RawResponse:    rawResponse,
		ExpectedSchema: expectedSchema,
	}
}

type ModelError struct {
	LLMError
	Model      string
	StatusCode int
}

func NewModelError(model string, statusCode int) *ModelError {
	return &ModelError{
		LLMError:   LLMError{BaseError{Message: fmt.Sprintf("Model %s returned error status %d", model, statusCode)}},
		Model:      model,
		StatusCode: statusCode,
	}
}

// Other Errors
type EnrichmentError struct {
	BaseError
	CompanyID int
	Step      string
	Reason    string
}

func NewEnrichmentError(companyID int, step string, reason string) *EnrichmentError {
	return &EnrichmentError{
		BaseError: BaseError{Message: fmt.Sprintf("Enrichment failed for company %d at step '%s': %s", companyID, step, reason)},
		CompanyID: companyID,
		Step:      step,
		Reason:    reason,
	}
}

type DatabaseError struct {
	BaseError
}

func NewDatabaseError(message string) *DatabaseError {
	return &DatabaseError{BaseError{Message: message}}
}
