package config

import (
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"
)

// Config holds the complete application configuration
// Loaded from config.yaml (business logic) and .env (secrets)
type Config struct {
	// ENV variables (secrets only)
	ConfigPath          string `env:"CONFIG_PATH" envDefault:"config.yaml"`
	DBPath              string `env:"DB_PATH" envDefault:"data/jobs.db"`
	SireneParquetPath   string `env:"SIRENE_PARQUET_PATH" envDefault:"data/sirene.parquet"`
	SireneULParquetPath string `env:"SIRENE_UL_PARQUET_PATH" envDefault:"data/sirene_ul.parquet"`
	OpenRouterAPIKey    string `env:"OPENROUTER_API_KEY,required"`
	GeminiAPIKey        string `env:"GEMINI_API_KEY,required"`
	ChromeExecutable    string `env:"CHROME_EXECUTABLE" envDefault:""`
	DuckDuckGoBaseURL   string `env:"DUCKDUCKGO_BASE_URL" envDefault:"https://html.duckduckgo.com/html/"`

	// Browser-related ENV variables
	BrowserCookiesPath string `env:"BROWSER_COOKIES_PATH" envDefault:""`
	BrowserDisplay     string `env:"BROWSER_DISPLAY" envDefault:""`
	BrowserHeadless    bool   `env:"BROWSER_HEADLESS" envDefault:"true"`
	BrowserBinaryPath  string `env:"BROWSER_BINARY_PATH" envDefault:""`

	// YAML configuration (business logic)
	LLM        LLMConfig        `yaml:"llm"`
	Enrichment EnrichmentConfig `yaml:"enrichment"`
	Cache      CacheConfig      `yaml:"cache"`
	Quality    QualityConfig    `yaml:"quality"`
	Scraping   ScrapingConfig   `yaml:"scraping"`
	Sirene     SireneConfig     `yaml:"sirene"`
	Monitoring MonitoringConfig `yaml:"monitoring"`

	// Backward compatibility - populated after loading
	Constants           ConstantsCompat `yaml:"-"`
	LLMPrimary          string          `yaml:"-"`
	LLMFallback         string          `yaml:"-"` // Single fallback string for legacy code
	OpenRouterRPM       int             `yaml:"-"`
	OpenRouterModel     string          `yaml:"-"`
	GeminiAPIModel      string          `yaml:"-"`
	ForceBrowserDomains string          `yaml:"-"` // Comma-separated for legacy code
}

// LLMConfig configures LLM providers and rate limiting
type LLMConfig struct {
	Models             ModelStrategies `yaml:"models"`
	EmergencyFallbacks []string        `yaml:"emergency_fallbacks"`
	RateLimits         RateLimitConfig `yaml:"rate_limits"`
}

// ModelStrategies defines model choices per task type
type ModelStrategies struct {
	Discovery  ModelStrategy `yaml:"discovery"`
	Extraction ModelStrategy `yaml:"extraction"`
	Ranking    ModelStrategy `yaml:"ranking"`
	Enrichment ModelStrategy `yaml:"enrichment"`
}

// ModelStrategy defines primary/fallback for a specific task
type ModelStrategy struct {
	Primary  string `yaml:"primary"`
	Fallback string `yaml:"fallback"`
	Provider string `yaml:"provider"` // "openrouter" or "gemini_api"
}

// RateLimitConfig defines rate limiting behavior
type RateLimitConfig struct {
	RequestsPerMinute       int            `yaml:"requests_per_minute"`
	RequestsPerDay          int            `yaml:"requests_per_day"`
	BurstSize               int            `yaml:"burst_size"`
	ProviderLimits          map[string]int `yaml:"provider_limits"`
	RespectRetryAfter       bool           `yaml:"respect_retry_after"`
	MaxBackoffSeconds       int            `yaml:"max_backoff_seconds"`
	EnableDynamicAdjustment bool           `yaml:"enable_dynamic_adjustment"`
}

// EnrichmentConfig configures the enrichment pipeline
type EnrichmentConfig struct {
	Parallelism int                   `yaml:"parallelism"`
	BatchSize   int                   `yaml:"batch_size"`
	Methods     EnrichmentMethods     `yaml:"methods"`
	Discovery   DiscoveryConfig       `yaml:"discovery"`
	Conditional ConditionalEnrichment `yaml:"conditional"`
}

// EnrichmentMethods toggles between batch and single processing
type EnrichmentMethods struct {
	BatchRanking     bool `yaml:"batch_ranking"`
	SingleRanking    bool `yaml:"single_ranking"`
	BatchEnrichment  bool `yaml:"batch_enrichment"`
	SingleEnrichment bool `yaml:"single_enrichment"`
}

// DiscoveryConfig configures URL discovery strategy
type DiscoveryConfig struct {
	Strategy                 string `yaml:"strategy"`
	UseGeminiSearchGrounding bool   `yaml:"use_gemini_search_grounding"`
	UseBrowserFallback       bool   `yaml:"use_browser_fallback"`
	SkipDDGSearch            bool   `yaml:"skip_ddg_search"`
	BrowserFallbackMinScore  int    `yaml:"browser_fallback_min_score"`
}

// ConditionalEnrichment configures score-based enrichment optimization
type ConditionalEnrichment struct {
	Enabled             bool                  `yaml:"enabled"`
	LowScoreThreshold   int                   `yaml:"low_score_threshold"`
	MaxProfilesToEnrich MaxProfilesByPriority `yaml:"max_profiles_to_enrich"`
}

// MaxProfilesByPriority defines how many profiles to enrich by score range
type MaxProfilesByPriority struct {
	HighPriority   int `yaml:"high_priority"`   // score >= 8
	MediumPriority int `yaml:"medium_priority"` // 6 <= score < 8
	LowPriority    int `yaml:"low_priority"`    // score < 6
}

// CacheConfig configures caching behavior
type CacheConfig struct {
	LLMResponses LLMCacheConfig `yaml:"llm_responses"`
	Scrape       ScrapeCache    `yaml:"scrape"`
}

// LLMCacheConfig configures LLM response caching
type LLMCacheConfig struct {
	Enabled  bool           `yaml:"enabled"`
	TTLHours map[string]int `yaml:"ttl_hours"` // task -> hours
}

// ScrapeCache configures web scraping cache
type ScrapeCache struct {
	Enabled bool `yaml:"enabled"`
}

// QualityConfig defines quality thresholds for scraping
type QualityConfig struct {
	HTTPMin      float64 `yaml:"http_min"`
	BrowserMin   float64 `yaml:"browser_min"`
	DiscoveryMin float64 `yaml:"discovery_min"`
	EnrichMin    float64 `yaml:"enrich_min"`
}

// ScrapingConfig configures web scraping behavior
type ScrapingConfig struct {
	UserAgent           string         `yaml:"user_agent"`
	ForceBrowserDomains []string       `yaml:"force_browser_domains"`
	Delays              ScrapingDelays `yaml:"delays"`
}

// ScrapingDelays defines timing for scraping operations
type ScrapingDelays struct {
	BrowserSettle  time.Duration `yaml:"browser_settle"`
	CookieClick    time.Duration `yaml:"cookie_click"`
	ScrollBase     time.Duration `yaml:"scroll_base"`
	ScrollVariance time.Duration `yaml:"scroll_variance"`
	RetryBackoff   time.Duration `yaml:"retry_backoff"`
	BatchSleep     time.Duration `yaml:"batch_sleep"`
}

// SireneConfig configures SIRENE data processing
type SireneConfig struct {
	TechNafPrefixes []string          `yaml:"tech_naf_prefixes"`
	NafLabels       map[string]string `yaml:"naf_labels"`
	HeadcountLevels map[string]int    `yaml:"headcount_levels"`
	HeadcountLabels map[string]string `yaml:"headcount_labels"`
}

// MonitoringConfig configures monitoring and alerts
type MonitoringConfig struct {
	EnableRateLimitAlerts bool      `yaml:"enable_rate_limit_alerts"`
	AlertThresholdPerHour int       `yaml:"alert_threshold_per_hour"`
	LogLevel              string    `yaml:"log_level"`
	TUI                   TUIConfig `yaml:"tui"`
}

// TUIConfig configures TUI behavior
type TUIConfig struct {
	EnableRateMonitor bool          `yaml:"enable_rate_monitor"`
	RefreshInterval   time.Duration `yaml:"refresh_interval"`
}

// Legacy interface methods (for backward compatibility)
func (c *Config) GetOpenRouterAPIKey() string { return c.OpenRouterAPIKey }
func (c *Config) GetGeminiAPIKey() string     { return c.GeminiAPIKey }

// GetOpenRouterModel returns the default OpenRouter model (for legacy code)
// TODO: Migrate code to use task-specific models
func (c *Config) GetOpenRouterModel() string {
	return c.LLM.Models.Extraction.Primary
}

// GetGeminiAPIModel returns the Gemini model (for legacy code)
func (c *Config) GetGeminiAPIModel() string {
	if c.LLM.Models.Discovery.Provider == "gemini_api" {
		return c.LLM.Models.Discovery.Primary
	}
	return "gemini-2.0-flash-exp"
}

// Load reads configuration from config.yaml and .env
func Load() *Config {
	// Load environment variables
	_ = godotenv.Load()

	cfg := &Config{}

	// Parse ENV variables
	if err := env.Parse(cfg); err != nil {
		log.Fatalf("Failed to parse environment variables: %v", err)
	}

	// Load YAML config
	configPath := cfg.ConfigPath
	data, err := os.ReadFile(configPath)
	if err != nil {
		log.Fatalf("Failed to read config file %s: %v", configPath, err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		log.Fatalf("Failed to unmarshal config.yaml: %v", err)
	}

	// Populate backward compatibility Constants field
	cfg.Constants = ConstantsCompat{
		UserAgent:         cfg.Scraping.UserAgent,
		QualityThresholds: cfg.Quality,
		Delays:            cfg.Scraping.Delays,
		Sirene:            cfg.Sirene,
	}

	// Populate legacy LLM fields for backward compatibility
	cfg.LLMPrimary = cfg.LLM.Models.Extraction.Primary
	cfg.LLMFallback = cfg.LLM.Models.Extraction.Fallback
	cfg.OpenRouterRPM = cfg.LLM.RateLimits.RequestsPerMinute
	cfg.OpenRouterModel = cfg.LLM.Models.Extraction.Primary
	cfg.GeminiAPIModel = cfg.GetGeminiAPIModel()
	cfg.ForceBrowserDomains = strings.Join(cfg.Scraping.ForceBrowserDomains, ",")

	// Validate configuration
	if err := cfg.Validate(); err != nil {
		log.Fatalf("Invalid configuration: %v", err)
	}

	return cfg
}

// Validate checks that the configuration is valid
func (c *Config) Validate() error {
	// Check API keys
	if c.OpenRouterAPIKey == "" && c.GeminiAPIKey == "" {
		return fmt.Errorf("at least one API key (OPENROUTER_API_KEY or GEMINI_API_KEY) must be set")
	}

	// Check rate limits
	if c.LLM.RateLimits.RequestsPerMinute <= 0 {
		return fmt.Errorf("requests_per_minute must be > 0, got %d", c.LLM.RateLimits.RequestsPerMinute)
	}

	if c.LLM.RateLimits.BurstSize < 1 {
		return fmt.Errorf("burst_size must be >= 1, got %d", c.LLM.RateLimits.BurstSize)
	}

	// Check parallelism
	if c.Enrichment.Parallelism <= 0 {
		return fmt.Errorf("enrichment.parallelism must be > 0, got %d", c.Enrichment.Parallelism)
	}

	// Warn about high parallelism with low rate limits
	if c.Enrichment.Parallelism > 5 && c.LLM.RateLimits.RequestsPerMinute < 100 {
		log.Printf("WARNING: High parallelism (%d) with low rate limit (%d RPM) may cause throttling",
			c.Enrichment.Parallelism, c.LLM.RateLimits.RequestsPerMinute)
	}

	return nil
}

// GetModelForTask returns the model strategy for a given task type
func (c *Config) GetModelForTask(task string) ModelStrategy {
	switch task {
	case "discovery", "discovery_llm", "discovery_gemini", "discovery_ddg":
		return c.LLM.Models.Discovery
	case "extract_company_info", "extract_people", "extract_links", "extract_urls_from_search":
		return c.LLM.Models.Extraction
	case "rank_contacts":
		return c.LLM.Models.Ranking
	case "enrich_profile", "enrich_profiles_batch":
		return c.LLM.Models.Enrichment
	default:
		// Default to extraction strategy
		return c.LLM.Models.Extraction
	}
}

// GetMaxProfilesToEnrich returns how many profiles to enrich based on score
func (c *Config) GetMaxProfilesToEnrich(relevanceScore int) int {
	if !c.Enrichment.Conditional.Enabled {
		return 3 // Default behavior
	}

	if relevanceScore >= 8 {
		return c.Enrichment.Conditional.MaxProfilesToEnrich.HighPriority
	} else if relevanceScore >= c.Enrichment.Conditional.LowScoreThreshold {
		return c.Enrichment.Conditional.MaxProfilesToEnrich.MediumPriority
	}
	return c.Enrichment.Conditional.MaxProfilesToEnrich.LowPriority
}

// Legacy compatibility - keep these for backward compatibility
type Constants struct {
	UserAgent         string
	QualityThresholds QualityConfig
	Delays            ScrapingDelays
	Sirene            SireneConfig
}

// GetConstants returns a legacy Constants struct for backward compatibility
func (c *Config) GetConstants() Constants {
	return Constants{
		UserAgent:         c.Scraping.UserAgent,
		QualityThresholds: c.Quality,
		Delays:            c.Scraping.Delays,
		Sirene:            c.Sirene,
	}
}

// Constants field for backward compatibility
func (c *Config) GetQualityThresholds() QualityConfig {
	return c.Quality
}

// For backward compatibility with code that accesses cfg.Constants
type ConstantsCompat struct {
	UserAgent         string
	QualityThresholds QualityConfig
	Delays            ScrapingDelays
	Sirene            SireneConfig
}
