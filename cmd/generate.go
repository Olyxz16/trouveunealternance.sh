package cmd

import (
	"context"
	"database/sql"
	"fmt"
	"jobhunter/internal/generator"
	"jobhunter/internal/llm"
	"log"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
)

var generateBatch int

func init() {
	generateCmd.Flags().IntVarP(&generateBatch, "batch", "b", 10, "Number of companies to generate drafts for")
	rootCmd.AddCommand(generateCmd)
}

var generateCmd = &cobra.Command{
	Use:   "generate",
	Short: "Generate outreach drafts for companies ready to contact",
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()
		runID := uuid.New().String()

		// 1. Load Profile
		prof, err := generator.LoadProfile("profile.json")
		if err != nil {
			log.Fatalf("Failed to load profile.json: %v", err)
		}

		// 2. Setup LLM
		var primary, fallback llm.Provider
		if cfg.LLMPrimary == "openrouter" {
			primary = llm.NewOpenRouterProvider(cfg.OpenRouterAPIKey, cfg.OpenRouterModel)
		} else {
			primary = llm.NewGeminiCLIProvider(cfg.GeminiCLIPath)
		}
		if cfg.LLMFallback != "" {
			if cfg.LLMFallback == "gemini_cli" {
				fallback = llm.NewGeminiCLIProvider(cfg.GeminiCLIPath)
			} else {
				fallback = llm.NewOpenRouterProvider(cfg.OpenRouterAPIKey, cfg.OpenRouterModel)
			}
		}
		llmClient := llm.NewClient(primary, fallback, cfg.OpenRouterRPM, database)

		gen := generator.NewGenerator(database, llmClient)

		// 3. Find candidates
		rows, err := database.Query("SELECT id, name, primary_contact_id FROM companies WHERE status = 'TO_CONTACT' LIMIT ?", generateBatch)
		if err != nil {
			log.Fatalf("Failed to query companies: %v", err)
		}
		defer rows.Close()

		count := 0
		for rows.Next() {
			var id int
			var name string
			var contactID sql.NullInt64
			if err := rows.Scan(&id, &name, &contactID); err != nil {
				continue
			}
			if !contactID.Valid {
				continue // skip companies with no primary contact
			}

			fmt.Printf("▶ Generating drafts for %s...\n", name)
			drafts, err := gen.GenerateDrafts(ctx, id, int(contactID.Int64), prof, runID)
			if err != nil {
				fmt.Printf("  ❌ Error: %v\n", err)
				continue
			}

			// Save to DB (simplified for now - real version should use a db method)
			// For V1, we just log or store in a simple way
			// The plan mentions a drafts table from migration 007.
			
			_, err = database.Exec(`
				INSERT INTO drafts (company_id, contact_id, type, subject, body, model)
				VALUES (?, ?, 'email', ?, ?, ?)
			`, id, int(contactID.Int64), drafts.Email.Subject, drafts.Email.Body, primary.Name())
			
			_, err = database.Exec(`
				INSERT INTO drafts (company_id, contact_id, type, body, model)
				VALUES (?, ?, 'linkedin', ?, ?)
			`, id, int(contactID.Int64), drafts.Linkedin.Body, primary.Name())

			if err == nil {
				fmt.Printf("  ✅ Drafts saved\n")
				count++
			} else {
				fmt.Printf("  ❌ Database error: %v\n", err)
			}
		}

		fmt.Printf("\n✓ Finished. Generated drafts for %d companies.\n", count)
	},
}
