package guesser

import (
	"fmt"
	"jobhunter/internal/db"
	"net"
	"strings"
)

type Guesser struct {
	db *db.DB
}

func NewGuesser(database *db.DB) *Guesser {
	return &Guesser{db: database}
}

func (g *Guesser) GenerateCandidates(firstName, lastName, domain string) []string {
	fn := strings.ToLower(firstName)
	ln := strings.ToLower(lastName)
	domain = strings.ToLower(domain)

	if domain == "" || fn == "" || ln == "" {
		return nil
	}

	patterns := []string{
		fmt.Sprintf("%s.%s@%s", fn, ln, domain),
		fmt.Sprintf("%s%s@%s", string(fn[0]), ln, domain),
		fmt.Sprintf("%s@%s", ln, domain),
		fmt.Sprintf("%s@%s", fn, domain),
		fmt.Sprintf("%s%s@%s", fn, ln, domain),
		fmt.Sprintf("%s.%s@%s", string(fn[0]), ln, domain),
	}

	return patterns
}

func (g *Guesser) VerifyDomain(domain string) bool {
	mx, err := net.LookupMX(domain)
	return err == nil && len(mx) > 0
}

func (g *Guesser) EnrichMissingEmails(batchSize int) (int, error) {
	// Find contacts with missing emails but having a linkedin_url or company domain
	// For now, let's just implement the logic to be called from CMD
	return 0, nil
}
