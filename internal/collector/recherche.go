package collector

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type RechercheResult struct {
	Name    string
	Website string
}

type RechercheClient struct {
	client *http.Client
}

func NewRechercheClient() *RechercheClient {
	return &RechercheClient{
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (r *RechercheClient) GetCompanyInfo(ctx context.Context, siren string) (RechercheResult, error) {
	url := fmt.Sprintf("https://recherche-entreprises.api.gouv.fr/search?q=%s", siren)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return RechercheResult{}, err
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return RechercheResult{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return RechercheResult{}, fmt.Errorf("recherche API error: %d", resp.StatusCode)
	}

	var data struct {
		Results []struct {
			NomComplet string `json:"nom_complet"`
			Website    string `json:"site_internet"`
		} `json:"results"`
	}

	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return RechercheResult{}, err
	}

	if len(data.Results) == 0 {
		return RechercheResult{}, fmt.Errorf("no results for siren %s", siren)
	}

	return RechercheResult{
		Name:    data.Results[0].NomComplet,
		Website: data.Results[0].Website,
	}, nil
}
