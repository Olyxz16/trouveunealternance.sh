package cmd

import (
	"fmt"
	"jobhunter/internal/scraper"
	"log"
	"time"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
)

func init() {
	rootCmd.AddCommand(loginCmd)
}

var loginCmd = &cobra.Command{
	Use:   "login",
	Short: "Open browser for manual LinkedIn login (run once to save session)",
	Run: func(cmd *cobra.Command, args []string) {
		logger, _ := zap.NewDevelopment()
		defer logger.Sync()

		fmt.Println("Starting browser in headed mode...")
		fmt.Println("Log into LinkedIn in the browser window that opens.")
		fmt.Println("The session will be saved automatically once you are logged in.")
		fmt.Println()

		// Always headed for login
		bf, err := scraper.NewBrowserFetcher(
			cfg.BrowserCookiesPath,
			cfg.BrowserDisplay,
			false, // headed
			cfg.BrowserBinaryPath,
			logger,
		)
		if err != nil {
			log.Fatalf("Failed to start browser: %v", err)
		}
		defer bf.Close()

		// Navigate to LinkedIn
		_, err = bf.Fetch(cmd.Context(), "https://www.linkedin.com/login")
		if err != nil {
			log.Fatalf("Failed to navigate to LinkedIn: %v", err)
		}

		fmt.Println("Browser is open. Log in now.")
		fmt.Println("Waiting for login (checking every 5 seconds)...")
		fmt.Println("(Looking for LinkedIn session cookie 'li_at')")

		// Poll for the li_at cookie — LinkedIn's session token.
		// Present only when logged in; absent on the login/challenge page.
		for i := 0; i < 60; i++ { // wait up to 5 minutes
			time.Sleep(5 * time.Second)

			loggedIn, err := bf.HasCookie("li_at", ".linkedin.com")
			if err != nil {
				continue
			}
			if loggedIn {
				fmt.Println()
				fmt.Println("✓ Login detected! Saving session...")
				// saveCookies is called inside Fetch, so already saved
				fmt.Printf("✓ Session saved to %s\n", cfg.BrowserCookiesPath)
				fmt.Println()
				fmt.Println("⚠ Keep this file private — it contains your LinkedIn session.")
				fmt.Println("  It is already gitignored via data/")
				return
			}
		}

		fmt.Println("Timed out waiting for login. Please try again.")
	},
}
