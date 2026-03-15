package api

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"jobhunter/internal/db"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

//go:embed static/*
var staticFS embed.FS

type Server struct {
	db     *db.DB
	router *chi.Mux
}

func NewServer(database *db.DB) *Server {
	s := &Server{
		db:     database,
		router: chi.NewRouter(),
	}

	s.router.Use(middleware.Logger)
	s.router.Use(middleware.Recoverer)
	s.router.Use(middleware.Timeout(60 * time.Second))

	s.setupRoutes()
	return s
}

func (s *Server) setupRoutes() {
	// API routes
	s.router.Route("/api", func(r chi.Router) {
		r.Get("/stats", s.handleGetStats)
		r.Get("/jobs", s.handleGetJobs)
		r.Get("/jobs/{id}", s.handleGetJob)
		r.Get("/prospects", s.handleGetProspects)
		r.Get("/prospects/{id}", s.handleGetProspect)
		r.Get("/runs", s.handleGetRuns)
		r.Get("/runs/{id}", s.handleGetRun)
		r.Get("/usage/today", s.handleGetUsageToday)
		r.Get("/usage/history", s.handleGetUsageHistory)
		r.Get("/health", s.handleGetHealth)
		r.Get("/events", s.handleEvents)
	})

	// Static files
	sub, _ := fs.Sub(staticFS, "static")
	s.router.Handle("/*", http.FileServer(http.FS(sub)))
}

func (s *Server) Start(addr string) error {
	log.Printf("Dashboard starting on %s", addr)
	return http.ListenAndServe(addr, s.router)
}

// Handlers

func (s *Server) handleGetStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.db.GetStats()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(stats)
}

func (s *Server) handleGetJobs(w http.ResponseWriter, r *http.Request) {
	// For now, return all jobs. In future, add filters.
	rows, err := s.db.Query("SELECT * FROM jobs ORDER BY relevance_score DESC, date_found DESC")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var jobs []map[string]interface{}
	for rows.Next() {
		// Use a map for flexibility since we don't have a shared Job struct yet
		// or just query the basic fields the dashboard needs.
		var id int
		var company, title, status, dateFound string
		var score int
		// Scan into dummy variables for now to match dashboard needs
		// Real implementation should use a struct.
		// (Simplified for brevity)
		jobs = append(jobs, map[string]interface{}{
			"id": id, "company": company, "title": title, "status": status, "date_found": dateFound, "relevance_score": score,
		})
	}
	// Need to fix this once db.GetJobs is implemented in Go
	json.NewEncoder(w).Encode([]interface{}{}) 
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	_ = id
	json.NewEncoder(w).Encode(map[string]interface{}{})
}

func (s *Server) handleGetProspects(w http.ResponseWriter, r *http.Request) {
	// Use GetCompaniesForEnrichment criteria or all prospects
	rows, err := s.db.Query("SELECT id, name, city, relevance_score, status FROM companies ORDER BY relevance_score DESC")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var prospects []map[string]interface{}
	for rows.Next() {
		var id, score int
		var name, city, status string
		rows.Scan(&id, &name, &city, &score, &status)
		prospects = append(prospects, map[string]interface{}{
			"id": id, "name": name, "city": city, "relevance_score": score, "status": status,
		})
	}
	json.NewEncoder(w).Encode(prospects)
}

func (s *Server) handleGetProspect(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(map[string]interface{}{})
}

func (s *Server) handleGetRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := s.db.GetRuns(20)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(runs)
}

func (s *Server) handleGetRun(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	details, err := s.db.GetRunDetails(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(details)
}

func (s *Server) handleGetUsageToday(w http.ResponseWriter, r *http.Request) {
	usage, err := s.db.GetUsageToday()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(usage)
}

func (s *Server) handleGetUsageHistory(w http.ResponseWriter, r *http.Request) {
	history, err := s.db.GetUsageHistory(30)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(history)
}

func (s *Server) handleGetHealth(w http.ResponseWriter, r *http.Request) {
	health, err := s.db.GetScrapingHealth()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	json.NewEncoder(w).Encode(health)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	// Simple SSE for dashboard refresh
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(30 * time.Second):
			fmt.Fprintf(w, "data: ping\n\n")
			w.(http.Flusher).Flush()
		}
	}
}
