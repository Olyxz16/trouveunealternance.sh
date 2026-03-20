package cmd

import (
	"context"
	"fmt"
	"jobhunter/internal/db"
	"jobhunter/internal/generator"
	"jobhunter/internal/llm"
	"log"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(generateCmd)
}

var generateCmd = &cobra.Command{
	Use:   "generate",
	Short: "Generate outreach drafts for high-score prospects",
	Run: func(cmd *cobra.Command, args []string) {
		profile, err := generator.LoadProfile("profile.json")
		if err != nil {
			log.Fatalf("Failed to load profile: %v", err)
		}

		// Use GORM to find candidates
		var contacts []db.Contact
		err = database.Table("contacts").
			Select("contacts.*").
			Joins("JOIN companies ON companies.id = contacts.company_id").
			Where("companies.relevance_score >= 7 AND companies.status = 'TO_CONTACT'").
			Find(&contacts).Error

		if err != nil {
			log.Fatalf("Failed to query candidates: %v", err)
		}

		if len(contacts) == 0 {
			fmt.Println("No candidates found for draft generation.")
			return
		}

		fmt.Printf("Found %d candidates. Generating drafts...\n", len(contacts))

		primary, fallback := llm.InitProviders(cfg.LLMPrimary, cfg.LLMFallback, cfg)
		llmClient := llm.NewClient(primary, fallback, cfg.OpenRouterRPM, database)
		gen := generator.NewGenerator(database, llmClient)

		runID := fmt.Sprintf("gen_%d", len(contacts))

		for _, c := range contacts {
			fmt.Printf("  - Generating for %s...\n", c.Name)
			drafts, err := gen.GenerateForContact(context.Background(), *profile, c.CompanyID, c.ID, runID)
			if err != nil {
				log.Printf("    ERROR: %v", err)
				continue
			}

			// Save email draft
			err = database.Create(&db.Draft{
				CompanyID: c.CompanyID,
				ContactID: &c.ID,
				Type:      "email",
				Subject:   drafts.Email.Subject,
				Body:      drafts.Email.Body,
				Status:    "pending",
			}).Error
			if err != nil {
				log.Printf("    ERROR saving email: %v", err)
			}

			// Save LinkedIn draft
			err = database.Create(&db.Draft{
				CompanyID: c.CompanyID,
				ContactID: &c.ID,
				Type:      "linkedin",
				Body:      drafts.Linkedin.Body,
				Status:    "pending",
			}).Error
			if err != nil {
				log.Printf("    ERROR saving linkedin: %v", err)
			}
		}

		fmt.Println("✓ Done.")
	},
}
