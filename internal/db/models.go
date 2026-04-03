package db

import (
	"time"

	"gorm.io/gorm"
)

// Company represents a company entity in the database.
type Company struct {
	ID        uint           `gorm:"primaryKey" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	Name           string `gorm:"not null" json:"name"`
	Siren          string `gorm:"uniqueIndex" json:"siren"`
	NAFCode        string `json:"naf_code"`
	NAFLabel       string `json:"naf_label"`
	City           string `json:"city"`
	Department     string `json:"department"`
	HeadcountRange string `json:"headcount_range"`
	Website        string `json:"website"`
	LinkedinURL    string `json:"linkedin_url"`
	CareersPageURL string `json:"careers_page_url"`
	TechStack      string `json:"tech_stack"`
	Status         string `gorm:"default:'NEW';index" json:"status"`
	RelevanceScore int    `gorm:"default:0;index" json:"relevance_score"`
	Notes          string `json:"notes"`
	DateFound      string `json:"date_found"`

	// Enriched fields
	CompanyType         string `gorm:"default:'UNKNOWN'" json:"company_type"`
	HasInternalTechTeam *bool  `json:"has_internal_tech_team"`
	TechTeamSignals     string `json:"tech_team_signals"`
	PrimaryContactID    uint   `json:"primary_contact_id"`
	CompanyEmail        string `json:"company_email"`

	// Relationships
	Contacts []Contact `gorm:"foreignKey:CompanyID" json:"contacts,omitempty"`
	Drafts   []Draft   `gorm:"foreignKey:CompanyID" json:"drafts,omitempty"`
}

// Contact represents an individual person at a company.
type Contact struct {
	ID        uint           `gorm:"primaryKey" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	CompanyID   uint   `gorm:"not null;index" json:"company_id"`
	Name        string `json:"name"`
	Role        string `json:"role"`
	Email       string `gorm:"index" json:"email"`
	LinkedinURL string `json:"linkedin_url"`
	Source      string `json:"source"`                         // linkedin, careers_page, manual, guessed
	Confidence  string `json:"confidence"`                     // verified, probable, guessed, hallucinated
	Status      string `gorm:"default:'active'" json:"status"` // active, bounced, unsubscribed
	Notes       string `json:"notes"`
}

// Job represents a job posting.
type Job struct {
	ID        uint           `gorm:"primaryKey" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	DateFound          string `json:"date_found"`
	SourceSite         string `json:"source_site"`
	Type               string `json:"type"` // DIRECT, COMPANY_LEAD
	Title              string `gorm:"not null" json:"title"`
	Company            string `gorm:"not null" json:"company"`
	Location           string `json:"location"`
	ContractType       string `json:"contract_type"`
	TechStack          string `json:"tech_stack"`
	DescriptionSummary string `json:"description_summary"`
	ApplyURL           string `json:"apply_url"`
	CareersPageURL     string `json:"careers_page_url"`
	RelevanceScore     int    `gorm:"default:0;index" json:"relevance_score"`
	Status             string `gorm:"default:'TO_APPLY';index" json:"status"`
}

// Draft represents a generated email or LinkedIn message.
type Draft struct {
	ID        uint           `gorm:"primaryKey" json:"id"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	DeletedAt gorm.DeletedAt `gorm:"index" json:"-"`

	CompanyID uint   `gorm:"not null;index" json:"company_id"`
	ContactID *uint  `json:"contact_id"`
	Type      string `json:"type"` // email, linkedin
	Subject   string `json:"subject"`
	Body      string `gorm:"not null" json:"body"`
	Status    string `gorm:"default:'pending'" json:"status"` // pending, sent, discarded
}

// PipelineRun tracks a specific execution session.
type PipelineRun struct {
	ID        string     `gorm:"primaryKey" json:"id"`
	Status    string     `gorm:"default:'running'" json:"status"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at"`

	Logs []RunLog `gorm:"foreignKey:RunID" json:"logs,omitempty"`
}

// RunLog tracks individual steps within a run.
type RunLog struct {
	ID        uint       `gorm:"primaryKey" json:"id"`
	RunID     string     `gorm:"not null;index" json:"run_id"`
	Step      string     `gorm:"not null" json:"step"`
	Status    string     `gorm:"not null" json:"status"`
	ErrorMsg  string     `json:"error_msg"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at"`
}

// ScrapeCache caches web content to avoid redundant fetches.
type ScrapeCache struct {
	URL       string    `gorm:"primaryKey" json:"url"`
	Content   string    `gorm:"not null" json:"content"`
	Method    string    `gorm:"not null" json:"method"`
	Quality   float64   `gorm:"not null" json:"quality"`
	CreatedAt time.Time `json:"created_at"`
}

// TokenUsage tracks LLM costs and usage.
type TokenUsage struct {
	ID               uint      `gorm:"primaryKey" json:"id"`
	RunID            string    `json:"run_id"`
	Task             string    `gorm:"not null" json:"task"`
	Model            string    `gorm:"not null" json:"model"`
	Provider         string    `gorm:"not null" json:"provider"`
	PromptTokens     int       `json:"prompt_tokens"`
	CompletionTokens int       `json:"completion_tokens"`
	CostUSD          float64   `json:"cost_usd"`
	IsEstimated      bool      `json:"is_estimated"`
	CreatedAt        time.Time `json:"created_at"`
}

// LLMResponseCache stores LLM responses to avoid redundant calls.
type LLMResponseCache struct {
	ID           uint      `gorm:"primaryKey" json:"id"`
	Provider     string    `gorm:"not null" json:"provider"`
	Model        string    `gorm:"not null" json:"model"`
	Task         string    `gorm:"not null;index" json:"task"`
	PromptHash   string    `gorm:"not null;index" json:"prompt_hash"`
	ResponseJSON string    `gorm:"not null" json:"response_json"`
	CreatedAt    time.Time `json:"created_at"`
	ExpiresAt    time.Time `gorm:"index" json:"expires_at"`
}

// QueueJob represents a queued work item for the daemon/worker system.
type QueueJob struct {
	ID          string     `gorm:"primaryKey" json:"id"`
	Type        string     `gorm:"not null;index" json:"type"`                     // enrich_company, scan_department, scan_region, re_enrich_failed, discover_urls
	Status      string     `gorm:"not null;index;default:'pending'" json:"status"` // pending, running, completed, failed, cancelled
	Payload     string     `gorm:"type:text" json:"payload"`                       // JSON-encoded job parameters
	Priority    int        `gorm:"default:0;index" json:"priority"`
	Attempts    int        `gorm:"default:0" json:"attempts"`
	MaxAttempts int        `gorm:"default:3" json:"max_attempts"`
	Error       string     `gorm:"type:text" json:"error"`
	CreatedAt   time.Time  `json:"created_at"`
	StartedAt   *time.Time `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at"`
	WorkerID    string     `gorm:"index" json:"worker_id"`
	NextRunAt   *time.Time `gorm:"index" json:"next_run_at"` // for retry with backoff
}

// RateLimit tracks token bucket state for distributed rate limiting.
type RateLimit struct {
	ID         string    `gorm:"primaryKey" json:"id"` // global, openrouter, gemini_api
	Tokens     float64   `gorm:"not null" json:"tokens"`
	LastRefill time.Time `json:"last_refill"`
	MaxTokens  float64   `gorm:"not null" json:"max_tokens"`
	RefillRate float64   `gorm:"not null" json:"refill_rate"`  // tokens per second
	DailyLimit int       `gorm:"default:0" json:"daily_limit"` // 0 = unlimited
	DailyUsed  int       `gorm:"default:0" json:"daily_used"`
	DailyReset time.Time `json:"daily_reset"`
}

// CompanyCooldown tracks per-company scrape cooldowns.
type CompanyCooldown struct {
	CompanyID     uint       `gorm:"primaryKey" json:"company_id"`
	LastScrapedAt *time.Time `json:"last_scraped_at"`
	NextAllowedAt *time.Time `gorm:"index" json:"next_allowed_at"`
	ScrapeCount   int        `gorm:"default:0" json:"scrape_count"`
	LastStatus    string     `json:"last_status"` // success, blocked, failed
}
