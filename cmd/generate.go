package cmd

import (
	"context"
	"fmt"
	"jobhunter/internal/db"
	"jobhunter/internal/generator"
	"jobhunter/internal/llm"
	"os"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
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
			zLogger.Error("Failed to load profile", zap.Error(err))
			os.Exit(1)
		}

		// Use GORM to find candidates
		var contacts []db.Contact
		err = database.Table("contacts").
			Select("contacts.*").
			Joins("JOIN companies ON companies.id = contacts.company_id").
			Where("companies.relevance_score >= 7 AND companies.status = 'TO_CONTACT'").
			Find(&contacts).Error

		if err != nil {
			zLogger.Error("Failed to query candidates", zap.Error(err))
			os.Exit(1)
		}

		if len(contacts) == 0 {
			fmt.Println("No candidates found for draft generation.")
			return
		}

		zLogger.Info("Generating drafts", zap.Int("candidate_count", len(contacts)))

		primary, fallback := llm.InitProviders(cfg.LLMPrimary, cfg.LLMFallback, cfg, zLogger)
		llmClient := llm.NewClient(primary, fallback, cfg.OpenRouterRPM, database, zLogger)
		gen := generator.NewGenerator(database, llmClient)

		runID := fmt.Sprintf("gen_%d", len(contacts))

		for _, c := range contacts {
			zLogger.Info("Generating for contact", zap.String("name", c.Name))
			drafts, err := gen.GenerateForContact(context.Background(), *profile, c.CompanyID, c.ID, runID)
			if err != nil {
				zLogger.Error("Generation failed", zap.String("name", c.Name), zap.Error(err))
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
				zLogger.Error("Failed to save email draft", zap.String("contact", c.Name), zap.Error(err))
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
				zLogger.Error("Failed to save linkedin draft", zap.String("contact", c.Name), zap.Error(err))
			}
		}

		zLogger.Info("Draft generation complete")
	},
}
