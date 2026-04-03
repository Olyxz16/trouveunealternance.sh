package scraper

import "testing"

func TestExtractPeopleFromLinkedInHTML(t *testing.T) {
	sample := `[Guillaume Texier](https://www.linkedin.com/in/guillaume-texier-1205031b?miniProfileUrn=urn)
  
  Relation de 3e niveau et plus · 3e
  
  Directeur technique - CRITT Informatique
  
[Bastien Diot](https://www.linkedin.com/in/diotbastien?miniProfileUrn=urn)
  
  Ingénieur en robotique`

	people := ExtractPeopleFromLinkedInHTML(sample)
	if len(people) != 2 {
		t.Errorf("Expected 2 people, got %d", len(people))
	}
	for _, p := range people {
		t.Logf("Name: %s, Role: %s, URL: %s", p.Name, p.Role, p.LinkedinURL)
	}
}
