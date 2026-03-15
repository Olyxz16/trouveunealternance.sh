package cmd

import (
	"fmt"
	"jobhunter/internal/guesser"
	"log"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(guessEmailsCmd)
}

var guessEmailsCmd = &cobra.Command{
	Use:   "guess-emails",
	Short: "Try to guess missing contact emails based on name and domain",
	Run: func(cmd *cobra.Command, args []string) {
		g := guesser.NewGuesser(database)

		rows, err := database.Query(`
			SELECT c.id, c.name, comp.website 
			FROM contacts c 
			JOIN companies comp ON c.company_id = comp.id 
			WHERE (c.email IS NULL OR c.email = '') 
			AND comp.website IS NOT NULL AND comp.website != ''
		`)
		if err != nil {
			log.Fatalf("Query failed: %v", err)
		}
		defer rows.Close()

		count := 0
		for rows.Next() {
			var id int
			var name, website string
			if err := rows.Scan(&id, &name, &website); err != nil {
				continue
			}

			// Extract domain from website
			u, err := url.Parse(website)
			if err != nil {
				continue
			}
			domain := strings.TrimPrefix(u.Host, "www.")
			if domain == "" {
				continue
			}

			// Split name
			parts := strings.Fields(name)
			if len(parts) < 2 {
				continue
			}
			first, last := parts[0], parts[1]

			candidates := g.GenerateCandidates(first, last, domain)
			if len(candidates) > 0 {
				// For now, just take the first pattern (fn.ln@domain)
				bestGuess := candidates[0]
				fmt.Printf("▶ Guessed %s for %s\n", bestGuess, name)
				
				_, err = database.Exec("UPDATE contacts SET email = ?, confidence = 'guessed' WHERE id = ?", bestGuess, id)
				if err == nil {
					count++
				}
			}
		}

		fmt.Printf("\n✓ Finished. Guessed %d emails.\n", count)
	},
}
