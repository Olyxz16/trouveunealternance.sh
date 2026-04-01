package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"
)

func init() {
	rootCmd.AddCommand(checkLimitsCmd)
}

var checkLimitsCmd = &cobra.Command{
	Use:   "check-limits",
	Short: "Check OpenRouter API key rate limits and credits",
	Run: func(cmd *cobra.Command, args []string) {
		key := cfg.OpenRouterAPIKey
		if key == "" {
			fmt.Println("No OPENROUTER_API_KEY configured.")
			return
		}

		client := &http.Client{Timeout: 10 * time.Second}
		req, _ := http.NewRequest("GET", "https://openrouter.ai/api/v1/key", nil)
		req.Header.Set("Authorization", "Bearer "+key)

		resp, err := client.Do(req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Request failed: %v\n", err)
			return
		}
		defer resp.Body.Close()

		var result struct {
			Data struct {
				Label          string   `json:"label"`
				Limit          *float64 `json:"limit"`
				LimitReset     *string  `json:"limit_reset"`
				LimitRemaining *float64 `json:"limit_remaining"`
				Usage          float64  `json:"usage"`
				UsageDaily     float64  `json:"usage_daily"`
				UsageWeekly    float64  `json:"usage_weekly"`
				UsageMonthly   float64  `json:"usage_monthly"`
				IsFreeTier     bool     `json:"is_free_tier"`
			} `json:"data"`
		}

		if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
			fmt.Fprintf(os.Stderr, "Failed to decode response: %v\n", err)
			return
		}

		fmt.Println("=== OpenRouter API Key Info ===")
		fmt.Printf("Label:        %s\n", result.Data.Label)
		fmt.Printf("Free tier:    %v\n", result.Data.IsFreeTier)
		fmt.Printf("Credit limit: %v\n", formatPtr(result.Data.Limit))
		fmt.Printf("Remaining:    %v\n", formatPtr(result.Data.LimitRemaining))
		fmt.Printf("Usage (all):  $%.4f\n", result.Data.Usage)
		fmt.Printf("Usage (day):  $%.4f\n", result.Data.UsageDaily)
		fmt.Printf("Usage (week): $%.4f\n", result.Data.UsageWeekly)
		fmt.Printf("Usage (month):$%.4f\n", result.Data.UsageMonthly)

		if result.Data.Limit != nil {
			pct := 0.0
			if *result.Data.Limit > 0 {
				pct = (*result.Data.LimitRemaining / *result.Data.Limit) * 100
			}
			fmt.Printf("\nCredits used: %.1f%% of limit\n", 100-pct)
		}

		fmt.Println("\nFree model limits:")
		if result.Data.IsFreeTier {
			fmt.Println("  - Free models: 20 RPM, 200 requests/day (no credits purchased)")
			fmt.Println("  - With $10+ credits: free model daily limit increases significantly")
		} else {
			fmt.Println("  - Paid account: higher limits on free models")
		}

		fmt.Printf("\nCurrent config:\n")
		fmt.Printf("  Model: %s\n", cfg.OpenRouterModel)
		fmt.Printf("  RPM limit: %d\n", cfg.OpenRouterRPM)
	},
}

func formatPtr(v interface{}) string {
	switch val := v.(type) {
	case *float64:
		if val == nil {
			return "unlimited"
		}
		return fmt.Sprintf("$%.4f", *val)
	case *string:
		if val == nil {
			return "never"
		}
		return *val
	default:
		return "unknown"
	}
}
