package config

import (
	"log"

	"github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
)

type Config struct {
	DBPath              string `env:"DB_PATH" envDefault:"data/jobs.db"`
	SireneParquetPath   string `env:"SIRENE_PARQUET_PATH" envDefault:"data/sirene_etablissements.parquet"`
	SireneULParquetPath string `env:"SIRENE_UL_PARQUET_PATH" envDefault:"data/sirene_unites_legales.parquet"`
	OpenRouterAPIKey    string `env:"OPENROUTER_API_KEY"`
	OpenRouterModel     string `env:"OPENROUTER_MODEL" envDefault:"google/gemini-2.5-flash-lite"`
	OpenRouterRPM       int    `env:"OPENROUTER_RPM" envDefault:"60"`
	GeminiCLIPath       string `env:"GEMINI_CLI_PATH" envDefault:"gemini"`
	LLMPrimary          string `env:"LLM_PRIMARY" envDefault:"openrouter"` // or gemini_cli
	LLMFallback         string `env:"LLM_FALLBACK" envDefault:"gemini_cli"`
	BrowserCookiesPath  string `env:"BROWSER_COOKIES_PATH"    envDefault:"data/browser_session.json"`
	BrowserDisplay      string `env:"BROWSER_DISPLAY"         envDefault:""`
	BrowserHeadless     bool   `env:"BROWSER_HEADLESS"        envDefault:"true"`
	BrowserBinaryPath   string `env:"BROWSER_BINARY_PATH"     envDefault:""`
	ForceBrowserDomains string `env:"FORCE_BROWSER_DOMAINS"   envDefault:"linkedin.com,duckduckgo.com"`
	GeminiAPIKey        string `env:"GEMINI_API_KEY"        envDefault:""`
	GeminiAPIModel      string `env:"GEMINI_API_MODEL"      envDefault:"gemini-2.0-flash-lite"`
}

func (c *Config) GetOpenRouterAPIKey() string { return c.OpenRouterAPIKey }
func (c *Config) GetOpenRouterModel() string  { return c.OpenRouterModel }
func (c *Config) GetGeminiAPIKey() string     { return c.GeminiAPIKey }
func (c *Config) GetGeminiAPIModel() string   { return c.GeminiAPIModel }
func (c *Config) GetGeminiCLIPath() string    { return c.GeminiCLIPath }

func Load() *Config {
	_ = godotenv.Load() // ignore error if .env doesn't exist

	cfg := Config{}
	if err := env.Parse(&cfg); err != nil {
		log.Fatalf("Failed to parse config: %v", err)
	}

	return &cfg
}
