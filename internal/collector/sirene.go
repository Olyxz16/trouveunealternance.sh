package collector

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"jobhunter/internal/config"
	"jobhunter/internal/db"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const SIRENE_PARQUET_URL = "https://object.files.data.gouv.fr/data-pipeline-open/siren/stock/StockEtablissement_utf8.parquet"
const SIRENE_UL_URL = "https://object.files.data.gouv.fr/data-pipeline-open/siren/stock/StockUniteLegale_utf8.parquet"

type SireneCollector struct {
	db        *db.DB
	cfg       *config.Config
	parquet   string
	ulParquet string
}

func NewSireneCollector(database *db.DB, cfg *config.Config) *SireneCollector {
	return &SireneCollector{
		db:        database,
		cfg:       cfg,
		parquet:   cfg.SireneParquetPath,
		ulParquet: cfg.SireneULParquetPath,
	}
}

func (s *SireneCollector) EnsureData(ctx context.Context) error {
	// Check etablissements
	if _, err := os.Stat(s.parquet); err != nil {
		log.Printf("SIRENE etablissements parquet missing. Downloading...")
		if err := s.download(ctx, SIRENE_PARQUET_URL, s.parquet); err != nil {
			return err
		}
	}

	// Check unites_legales
	if _, err := os.Stat(s.ulParquet); err != nil {
		log.Printf("SIRENE unites_legales parquet missing. Downloading from %s...", SIRENE_UL_URL)
		if err := s.download(ctx, SIRENE_UL_URL, s.ulParquet); err != nil {
			return err
		}
	}

	return nil
}

func (s *SireneCollector) download(ctx context.Context, url, dest string) error {
	dir := filepath.Dir(dest)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("failed to download from %s: status %d", url, resp.StatusCode)
	}

	out, err := os.Create(dest)
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
	
	query := fmt.Sprintf(`
		SELECT 
			e.siren, 
			e.siret, 
			COALESCE(
				NULLIF(TRIM(e.enseigne1Etablissement), ''),
				NULLIF(TRIM(e.denominationUsuelleEtablissement), ''),
				NULLIF(TRIM(ul.denominationUniteLegale), ''),
				NULLIF(TRIM(ul.sigleUniteLegale), '')
			) AS name_raw,
			ul.denominationUniteLegale as legal_name,
			ul.sigleUniteLegale as acronym,
			e.activitePrincipaleEtablissement as naf_code,
			e.trancheEffectifsEtablissement as headcount_code,
			e.codePostalEtablissement as zip,
			e.libelleCommuneEtablissement as city
		FROM read_parquet('%s') e
		JOIN read_parquet('%s') ul ON e.siren = ul.siren
		WHERE e.etatAdministratifEtablissement = 'A'
		AND ul.etatAdministratifUniteLegale = 'A'
		AND SUBSTR(e.codePostalEtablissement, 1, 2) IN (%s)
	`, s.parquet, s.ulParquet, deptList)

	cmd := exec.CommandContext(ctx, "duckdb", "-csv", "-c", query)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, 0, err
	}

	if err := cmd.Start(); err != nil {
		return 0, 0, err
	}

	reader := csv.NewReader(stdout)
	_, err = reader.Read() // skip header
	if err != nil {
		return 0, 0, err
	}

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
		nameRaw := record[2]
		naf := record[5]
		hcCode := record[6]
		zip := record[7]
		city := record[8]

		hcVal := s.getMinHeadcount(hcCode)
		if hcVal < minHeadcount {
			continue
		}
		if hcCode == "NN" || hcCode == "00" || hcCode == "" {
			continue
		}

		cleanNAF := strings.ReplaceAll(naf, ".", "")
		companyType := "UNKNOWN"
		status := "NEW"

		isTech := false
		for _, prefix := range s.cfg.Constants.Sirene.TechNafPrefixes {
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
			continue
		}

		totalFound++

		c := &db.Company{
			Name:           cleanCompanyName(nameRaw),
			Siren:          siren,
			NAFCode:        naf,
			NAFLabel:       s.cfg.Constants.Sirene.NafLabels[cleanNAF],
			City:           city,
			Department:     zip[:2],
			HeadcountRange: s.headcountLabel(hcCode),
			CompanyType:    companyType,
			Status:         status,
		}

		_, isNew, err := s.db.UpsertCompany(c)
		if err != nil {
			log.Printf("Failed to upsert company %s: %v", c.Name, err)
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

func (s *SireneCollector) getMinHeadcount(code string) int {
	if val, ok := s.cfg.Constants.Sirene.HeadcountLevels[code]; ok {
		return val
	}
	return 0
}

func (s *SireneCollector) headcountLabel(code string) string {
	if l, ok := s.cfg.Constants.Sirene.HeadcountLabels[code]; ok {
		return l
	}
	return code
}
