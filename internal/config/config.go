package config

import (
	"log"
	"os"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
	"gopkg.in/yaml.v3"
)

type Config struct {
	DBPath              string `env:"DB_PATH" envDefault:"data/jobs.db"`
	SireneParquetPath   string `env:"SIRENE_PARQUET_PATH" envDefault:"data/sirene_etablissements.parquet"`
	SireneULParquetPath string `env:"SIRENE_UL_PARQUET_PATH" envDefault:"data/sirene_unites_legales.parquet"`
	OpenRouterAPIKey    string `env:"OPENROUTER_API_KEY"`
	OpenRouterModel     string `env:"OPENROUTER_MODEL" envDefault:"google/gemini-2.0-flash-lite:free"`
	OpenRouterRPM       int    `env:"OPENROUTER_RPM" envDefault:"60"`
	LLMPrimary          string `env:"LLM_PRIMARY" envDefault:"openrouter"`
	LLMFallback         string `env:"LLM_FALLBACK" envDefault:"openrouter"`
	BrowserCookiesPath  string `env:"BROWSER_COOKIES_PATH"    envDefault:"data/browser_session.json"`
	BrowserDisplay      string `env:"BROWSER_DISPLAY"         envDefault:""`
	BrowserHeadless     bool   `env:"BROWSER_HEADLESS"        envDefault:"true"`
	BrowserBinaryPath   string `env:"BROWSER_BINARY_PATH"     envDefault:""`
	ForceBrowserDomains string `env:"FORCE_BROWSER_DOMAINS"   envDefault:"linkedin.com,duckduckgo.com"`
	GeminiAPIKey        string `env:"GEMINI_API_KEY"        envDefault:""`
	GeminiAPIModel      string `env:"GEMINI_API_MODEL"      envDefault:"gemini-2.0-flash-lite"`

	Constants Constants `yaml:",inline"`
}

type Constants struct {
	UserAgent         string `yaml:"user_agent"`
	QualityThresholds struct {
		HTTPMin      float64 `yaml:"http_min"`
		BrowserMin   float64 `yaml:"browser_min"`
		DiscoveryMin float64 `yaml:"discovery_min"`
		EnrichMin    float64 `yaml:"enrich_min"`
	} `yaml:"quality_thresholds"`

	Delays struct {
		BrowserSettle  time.Duration `yaml:"browser_settle"`
		CookieClick    time.Duration `yaml:"cookie_click"`
		ScrollBase     time.Duration `yaml:"scroll_base"`
		ScrollVariance time.Duration `yaml:"scroll_variance"`
		RetryBackoff   time.Duration `yaml:"retry_backoff"`
		BatchSleep     time.Duration `yaml:"batch_sleep"`
	} `yaml:"delays"`

	Sirene struct {
		TechNafPrefixes []string          `yaml:"tech_naf_prefixes"`
		NafLabels       map[string]string `yaml:"naf_labels"`
		HeadcountLevels map[string]int    `yaml:"headcount_levels"`
		HeadcountLabels map[string]string `yaml:"headcount_labels"`
	} `yaml:"sirene"`
}

func (c *Config) GetOpenRouterAPIKey() string { return c.OpenRouterAPIKey }
func (c *Config) GetOpenRouterModel() string  { return c.OpenRouterModel }
func (c *Config) GetGeminiAPIKey() string     { return c.GeminiAPIKey }
func (c *Config) GetGeminiAPIModel() string   { return c.GeminiAPIModel }

func Load() *Config {
	_ = godotenv.Load()

	cfg := Config{}
	if err := env.Parse(&cfg); err != nil {
		log.Fatalf("Failed to parse config: %v", err)
	}

	// Load constants from YAML
	constPath := "internal/config/constants.yaml"
	data, err := os.ReadFile(constPath)
	if err != nil {
		log.Fatalf("Failed to read constants.yaml: %v", err)
	}

	if err := yaml.Unmarshal(data, &cfg.Constants); err != nil {
		log.Fatalf("Failed to unmarshal constants.yaml: %v", err)
	}

	return &cfg
}
