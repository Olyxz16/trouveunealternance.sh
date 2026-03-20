package guesser

import (
	"context"
	"fmt"
	"strings"
)

type EmailGuesser struct{}

func NewEmailGuesser() *EmailGuesser {
	return &EmailGuesser{}
}

func (g *EmailGuesser) Guess(ctx context.Context, fullName, website string) (string, error) {
	// Dummy implementation for now to satisfy the command
	// In reality this would try patterns or use an API
	if fullName == "" || website == "" {
		return "", nil
	}
	
	domain := strings.TrimPrefix(website, "http://")
	domain = strings.TrimPrefix(domain, "https://")
	domain = strings.TrimPrefix(domain, "www.")
	domain = strings.Split(domain, "/")[0]

	parts := strings.Split(strings.ToLower(fullName), " ")
	if len(parts) < 2 {
		return fmt.Sprintf("%s@%s", parts[0], domain), nil
	}

	return fmt.Sprintf("%s.%s@%s", parts[0], parts[1], domain), nil
}
