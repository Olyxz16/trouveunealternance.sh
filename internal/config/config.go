package config

import (
	"log"

	"github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
)

type Config struct {
	DBPath             string `env:"DB_PATH" envDefault:"data/jobs.db"`
	SireneParquetPath  string `env:"SIRENE_PARQUET_PATH" envDefault:"data/sirene.parquet"`
	OpenRouterAPIKey   string `env:"OPENROUTER_API_KEY"`
	OpenRouterModel    string `env:"OPENROUTER_MODEL" envDefault:"google/gemini-2.5-flash-lite"`
	OpenRouterRPM      int    `env:"OPENROUTER_RPM" envDefault:"60"`
	GeminiCLIPath      string `env:"GEMINI_CLI_PATH" envDefault:"gemini"`
	LLMPrimary         string `env:"LLM_PRIMARY" envDefault:"openrouter"` // or gemini_cli
	LLMFallback        string `env:"LLM_FALLBACK" envDefault:"gemini_cli"`
	MCPHost            string `env:"MCP_HOST" envDefault:"http://localhost:3000"`
	ForceMCPDomains    string `env:"FORCE_MCP_DOMAINS" envDefault:"linkedin.com"`
}

func Load() *Config {
	_ = godotenv.Load() // ignore error if .env doesn't exist

	cfg := Config{}
	if err := env.Parse(&cfg); err != nil {
		log.Fatalf("Failed to parse config: %v", err)
	}

	return &cfg
}
