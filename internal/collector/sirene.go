package collector

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"jobhunter/internal/db"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const SIRENE_PARQUET_URL = "https://www.data.gouv.fr/fr/datasets/r/a29c1297-1f92-4e2a-8f6b-8c902ce96c5f"

var NAF_LABELS = map[string]string{
	"6201Z": "Programmation informatique",
	"6202A": "Conseil en systèmes et logiciels informatiques",
	"6202B": "Tierce maintenance de systèmes et d'applications informatiques",
	"6203Z": "Gestion d'installations informatiques",
	"6209Z": "Autres activités informatiques",
	"6311Z": "Traitement de données, hébergement et activités connexes",
	"6312Z": "Portails Internet",
}

var TECH_NAF_PREFIXES = []string{"62", "63"}

type SireneCollector struct {
	db       *db.DB
	parquet  string
}

func NewSireneCollector(database *db.DB, parquetPath string) *SireneCollector {
	return &SireneCollector{
		db:      database,
		parquet: parquetPath,
	}
}

func (s *SireneCollector) EnsureData(ctx context.Context) error {
	if _, err := os.Stat(s.parquet); err == nil {
		return nil
	}

	log.Printf("SIRENE parquet missing. Downloading from %s...", SIRENE_PARQUET_URL)
	
	dir := filepath.Dir(s.parquet)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "GET", SIRENE_PARQUET_URL, nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download SIRENE: status %d", resp.StatusCode)
	}

	out, err := os.Create(s.parquet)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}

func (s *SireneCollector) Scan(ctx context.Context, departments []string, minHeadcount int) (int, int, error) {
	if err := s.EnsureData(ctx); err != nil {
		return 0, 0, fmt.Errorf("failed to ensure sirene data: %w", err)
	}

	deptList := "'" + strings.Join(departments, "','") + "'"
	
	// DuckDB query to filter SIRENE parquet
	// trancheEffectifsEtablissement codes: https://www.sirene.fr/static-resources/doc/v_sommaire_syntaxe_9-9.pdf
	// 00: 0, 01: 1-2, 02: 3-5, 03: 6-9, 11: 10-19, 12: 20-49, 21: 50-99, 22: 100-199, etc.
	
	query := fmt.Sprintf(`
		SELECT 
			siren, 
			siret, 
			COALESCE(denominationUsuelleEtablissement, enseigne1Etablissement, enseigne2Etablissement, enseigne3Etablissement, 'Company ' || siren) as name,
			activitePrincipaleEtablissement as naf_code,
			trancheEffectifsEtablissement as headcount_code,
			codePostalEtablissement as zip,
			libelleCommuneEtablissement as city
		FROM read_parquet('%s')
		WHERE etatAdministratifEtablissement = 'A'
		AND SUBSTR(codePostalEtablissement, 1, 2) IN (%s)
	`, s.parquet, deptList)

	cmd := exec.CommandContext(ctx, "duckdb", "-csv", "-c", query)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, 0, err
	}

	if err := cmd.Start(); err != nil {
		return 0, 0, err
	}

	reader := csv.NewReader(stdout)
	header, err := reader.Read() // skip header
	if err != nil {
		return 0, 0, err
	}
	_ = header

	totalFound := 0
	newAdded := 0

	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Printf("Error reading CSV from duckdb: %v", err)
			continue
		}

		siren := record[0]
		siret := record[1]
		name := record[2]
		naf := record[3]
		hcCode := record[4]
		zip := record[5]
		city := record[6]

		hcVal := getMinHeadcount(hcCode)
		if hcVal < minHeadcount && hcCode != "" && hcCode != "NN" && hcCode != "00" {
			continue
		}

		cleanNAF := strings.ReplaceAll(naf, ".", "")
		companyType := "UNKNOWN"
		status := "NEW"

		isTech := false
		for _, prefix := range TECH_NAF_PREFIXES {
			if strings.HasPrefix(cleanNAF, prefix) {
				isTech = true
				break
			}
		}

		if isTech {
			companyType = "TECH"
		} else if hcVal >= 100 {
			companyType = "UNKNOWN"
		} else {
			// Skip small non-tech
			continue
		}

		totalFound++

		c := &db.Company{
			Name:           name,
			Siren:          db.ToNullString(siren),
			Siret:          db.ToNullString(siret),
			NAFCode:        db.ToNullString(naf),
			NAFLabel:       db.ToNullString(NAF_LABELS[cleanNAF]),
			City:           db.ToNullString(city),
			Department:     db.ToNullString(zip[:2]),
			HeadcountRange: db.ToNullString(headcountLabel(hcCode)),
			Source:         db.ToNullString("sirene"),
			CompanyType:    companyType,
			Status:         status,
		}

		_, isNew, err := s.db.UpsertCompany(c)
		if err != nil {
			log.Printf("Failed to upsert company %s: %v", name, err)
			continue
		}
		if isNew {
			newAdded++
		}
	}

	if err := cmd.Wait(); err != nil {
		return totalFound, newAdded, err
	}

	return totalFound, newAdded, nil
}

func getMinHeadcount(code string) int {
	m := map[string]int{
		"03": 6, "11": 10, "12": 20, "21": 50, "22": 100,
		"31": 200, "32": 250, "41": 500, "42": 1000,
		"51": 2000, "52": 5000, "53": 10000,
	}
	return m[code]
}

func headcountLabel(code string) string {
	labels := map[string]string{
		"NN": "0", "00": "0", "01": "1-2", "02": "3-5", "03": "6-9",
		"11": "10-19", "12": "20-49", "21": "50-99", "22": "100-199",
		"31": "200-249", "32": "250-499", "41": "500-999", "42": "1000-1999",
		"51": "2000-4999", "52": "5000-9999", "53": "10000+",
	}
	if l, ok := labels[code]; ok {
		return l
	}
	return code
}
