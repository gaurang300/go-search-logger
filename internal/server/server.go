package server

import (
	"go-search-logger/internal/searchlogger"
	"log"
	"net/http"
)

type Server struct {
	Logger *searchlogger.Logger
}

func NewServer(logger *searchlogger.Logger) *Server {
	return &Server{Logger: logger}
}

func (s *Server) Start(addr string) error {
	http.HandleFunc("/search", s.searchHandler)
	log.Printf("Listening on %s", addr)
	return http.ListenAndServe(addr, nil)
}

func (s *Server) searchHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, "error parsing form", http.StatusBadRequest)
		return
	}

	query := r.FormValue("q")
	if query == "" {
		http.Error(w, "missing query parameter q", http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	userID := r.FormValue("user_id")

	userAgent := r.UserAgent()

	if err := s.Logger.LogSearch(ctx, userID, userAgent, query); err != nil {
		log.Printf("error logging search: %v", err)
		http.Error(w, "error logging search", http.StatusInternalServerError)
		return
	}

	w.Write([]byte("Query logged"))
}
