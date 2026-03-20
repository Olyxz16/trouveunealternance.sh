package cmd

import (
	"context"
	"fmt"
	"jobhunter/internal/db"
	"jobhunter/internal/guesser"
	"log"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(guessEmailsCmd)
}

var guessEmailsCmd = &cobra.Command{
	Use:   "guess-emails",
	Short: "Attempt to guess business emails for contacts",
	Run: func(cmd *cobra.Command, args []string) {
		var contacts []db.Contact
		err := database.Table("contacts").
			Select("contacts.*").
			Joins("JOIN companies ON companies.id = contacts.company_id").
			Where("contacts.email IS NULL OR contacts.email = ''").
			Where("companies.website IS NOT NULL AND companies.website != ''").
			Find(&contacts).Error

		if err != nil {
			log.Fatalf("Failed to query contacts: %v", err)
		}

		if len(contacts) == 0 {
			fmt.Println("No contacts found needing email guessing.")
			return
		}

		fmt.Printf("Guessing emails for %d contacts...\n", len(contacts))

		g := guesser.NewEmailGuesser()

		for _, c := range contacts {
			// Need to get company for website
			var comp db.Company
			if err := database.First(&comp, c.CompanyID).Error; err != nil {
				continue
			}

			fmt.Printf("  - Guessing for %s at %s...\n", c.Name, comp.Name)
			email, err := g.Guess(context.Background(), c.Name, comp.Website)
			if err != nil {
				log.Printf("    ERROR: %v", err)
				continue
			}

			if email != "" {
				fmt.Printf("    ✓ Found: %s\n", email)
				err = database.Model(&db.Contact{}).Where("id = ?", c.ID).Updates(map[string]interface{}{
					"email":      email,
					"confidence": "guessed",
					"source":     "guessed",
				}).Error
				if err != nil {
					log.Printf("    ERROR saving: %v", err)
				}
			} else {
				fmt.Println("    ✗ Could not determine email pattern.")
			}
		}

		fmt.Println("✓ Done.")
	},
}
