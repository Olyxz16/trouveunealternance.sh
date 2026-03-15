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

const SIRENE_PARQUET_URL = "https://object.files.data.gouv.fr/data-pipeline-open/siren/stock/StockEtablissement_utf8.parquet"

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
	db        *db.DB
	parquet   string
	ulParquet string
}

func NewSireneCollector(database *db.DB, parquetPath, ulParquetPath string) *SireneCollector {
	return &SireneCollector{
		db:        database,
		parquet:   parquetPath,
		ulParquet: ulParquetPath,
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

const SIRENE_UL_URL = "https://object.files.data.gouv.fr/data-pipeline-open/siren/stock/StockUniteLegale_utf8.parquet"

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
		siret := record[1]
		nameRaw := record[2]
		legalName := record[3]
		acronym := record[4]
		naf := record[5]
		hcCode := record[6]
		zip := record[7]
		city := record[8]

		hcVal := getMinHeadcount(hcCode)
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
			continue
		}

		totalFound++

		c := &db.Company{
			Name:           cleanCompanyName(nameRaw),
			LegalName:      db.ToNullString(legalName),
			Acronym:        db.ToNullString(acronym),
			NameNormalized: db.ToNullString(normalizeName(nameRaw)),
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
