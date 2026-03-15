package generator

import (
	"encoding/json"
	"os"
)

type Project struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Stack       []string `json:"stack"`
}

type Profile struct {
	Name         string    `json:"name"`
	School       string    `json:"school"`
	Skills       []string  `json:"skills"`
	Projects     []Project `json:"projects"`
	Availability string    `json:"availability"`
	Duration     string    `json:"duration"`
	Interests    []string  `json:"interests"`
}

func LoadProfile(path string) (*Profile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var p Profile
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, err
	}

	return &p, nil
}
