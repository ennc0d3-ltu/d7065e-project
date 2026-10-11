package registry

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
)

// Handler is the registry's HTTP API:
//
//	GET  /active                    the active bundle (404 if none)
//	GET  /bundles                   every version
//	POST /bundles                   publish a candidate
//	POST /bundles/{v}/promote       {"by": "<approver>"}
//	POST /rollback                  {"by": "<approver>"}
func Handler(s *Store) http.Handler {
	mux := http.NewServeMux()
	write := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	approver := func(w http.ResponseWriter, r *http.Request) (string, bool) {
		var req struct{ By string }
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<10)).Decode(&req); err != nil || req.By == "" {
			http.Error(w, `body must be {"by": "<approver>"}`, http.StatusBadRequest)
			return "", false
		}
		return req.By, true
	}
	mux.HandleFunc("GET /active", func(w http.ResponseWriter, _ *http.Request) {
		if b, ok := s.Active(); ok {
			write(w, http.StatusOK, b)
			return
		}
		http.Error(w, "no active bundle", http.StatusNotFound)
	})
	mux.HandleFunc("GET /bundles", func(w http.ResponseWriter, _ *http.Request) { write(w, http.StatusOK, s.List()) })
	mux.HandleFunc("POST /bundles", func(w http.ResponseWriter, r *http.Request) {
		var b Bundle
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8<<20)).Decode(&b); err != nil {
			http.Error(w, "bad body", http.StatusBadRequest)
			return
		}
		v, err := s.Publish(b)
		if err != nil {
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
			return
		}
		write(w, http.StatusCreated, map[string]int{"Version": v})
	})
	mux.HandleFunc("POST /bundles/{v}/promote", func(w http.ResponseWriter, r *http.Request) {
		v, err := strconv.Atoi(r.PathValue("v"))
		by, ok := approver(w, r)
		if !ok {
			return
		}
		if err != nil {
			http.Error(w, "bad version", http.StatusBadRequest)
			return
		}
		switch err := s.Promote(v, by); {
		case errors.Is(err, ErrNotFound):
			http.Error(w, err.Error(), http.StatusNotFound)
		case err != nil:
			http.Error(w, err.Error(), http.StatusUnprocessableEntity)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	})
	mux.HandleFunc("POST /rollback", func(w http.ResponseWriter, r *http.Request) {
		by, ok := approver(w, r)
		if !ok {
			return
		}
		if err := s.Rollback(by); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}
