package cmd

import (
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(setupCmd)
}

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "First-time setup wizard",
	Run: func(cmd *cobra.Command, args []string) {
		var (
			name         string
			school       string
			skills       string
			availability string
			duration     string
			openRouterKey string
		)

		form := huh.NewForm(
			huh.NewGroup(
				huh.NewInput().
					Title("Your Name").
					Value(&name),
				huh.NewInput().
					Title("Your School").
					Value(&school),
				huh.NewInput().
					Title("Skills (comma-separated)").
					Value(&skills),
			),
			huh.NewGroup(
				huh.NewInput().
					Title("Availability Date").
					Value(&availability),
				huh.NewInput().
					Title("Internship Duration").
					Value(&duration),
			),
			huh.NewGroup(
				huh.NewInput().
					Title("OpenRouter API Key").
					EchoMode(huh.EchoModePassword).
					Value(&openRouterKey),
			),
		)

		err := form.Run()
		if err != nil {
			log.Fatal(err)
		}

		// Write to .env
		envContent := fmt.Sprintf(`OPENROUTER_API_KEY=%s
OPENROUTER_MODEL=google/gemini-2.5-flash-lite
OPENROUTER_RPM=60
YOUR_NAME=%s
YOUR_SCHOOL=%s
YOUR_SKILLS=%s
START_DATE=%s
INTERNSHIP_DURATION=%s
`, openRouterKey, name, school, skills, availability, duration)

		err = os.WriteFile(".env", []byte(envContent), 0600)
		if err != nil {
			log.Fatalf("Failed to write .env: %v", err)
		}

		fmt.Println("\n✓ Setup complete! Configuration saved to .env")
		
		// Create profile.json if it doesn't exist
		if _, err := os.Stat("profile.json"); os.IsNotExist(err) {
			profileJSON := fmt.Sprintf(`{
  "name": "%s",
  "school": "%s",
  "skills": [%s],
  "projects": [],
  "availability": "%s",
  "duration": "%s",
  "interests": []
}`, name, school, quoteList(skills), availability, duration)
			_ = os.WriteFile("profile.json", []byte(profileJSON), 0644)
			fmt.Println("✓ profile.json initialized with your info.")
		}
	},
}

func quoteList(s string) string {
	parts := strings.Split(s, ",")
	for i, p := range parts {
		parts[i] = fmt.Sprintf("\"%s\"", strings.TrimSpace(p))
	}
	return strings.Join(parts, ", ")
}
