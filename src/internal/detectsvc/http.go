package detectsvc

import (
	"encoding/json"
	"errors"
	"net/http"
)

// OperatorHandler serves the operator's actions (FR4) and a status view:
//
//	POST /operator/{level}/{room}/{confirm|cancel|release}
//	GET  /status
//
// An unknown action is a 400 and an unknown room a 404. The service itself
// decides whether an action has any effect (a confirm with no warning is a
// no-op), and every action is recorded in the audit trail.
func (s *Service) OperatorHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /operator/{level}/{room}/{action}", func(w http.ResponseWriter, r *http.Request) {
		var act func(*Service, string) error
		switch r.PathValue("action") {
		case "confirm":
			act = func(s *Service, k string) error { return s.Confirm(r.Context(), k) }
		case "cancel":
			act = func(s *Service, k string) error { return s.Cancel(r.Context(), k) }
		case "release":
			act = func(s *Service, k string) error { return s.Release(r.Context(), k) }
		default:
			http.Error(w, "action must be confirm, cancel or release", http.StatusBadRequest)
			return
		}
		key := r.PathValue("level") + "/" + r.PathValue("room")
		if err := act(s, key); err != nil {
			if errors.Is(err, ErrUnknownRoom) {
				http.NotFound(w, r)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /status", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(s.Rooms())
	})
	return mux
}
