package evac

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"sync"
	"time"
)

// FireRooms returns the "<level>/<room>" keys whose response is on.
type FireRooms func(ctx context.Context) (map[string]bool, error)

// NewProxy returns a handler that stands in for BuildSim towards the people
// simulator. Everything is forwarded as is, except PUT /api/entities and PUT
// /api/occupancy, which are rewritten so people leave burning rooms. If the
// fire information is unavailable, or a body cannot be parsed, the request is
// forwarded unchanged: the proxy never blocks or invents the people feed.
func NewProxy(upstream string, eng *Engine, fire FireRooms, now func() float64) http.Handler {
	target, err := url.Parse(upstream)
	if err != nil {
		panic(err)
	}
	rp := httputil.NewSingleHostReverseProxy(target)
	client := &http.Client{Timeout: 5 * time.Second}

	var mu sync.Mutex
	var lastOcc map[string]any
	lastVersion := -1

	forward := func(w http.ResponseWriter, r *http.Request, body []byte) {
		req, err := http.NewRequestWithContext(r.Context(), r.Method, upstream+r.URL.RequestURI(), bytes.NewReader(body))
		if err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			http.Error(w, "buildsim unreachable", http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", resp.Header.Get("Content-Type"))
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("PUT /api/entities", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
		var ents []map[string]any
		rooms, ferr := fire(r.Context())
		if ferr != nil || json.Unmarshal(body, &ents) != nil {
			forward(w, r, body)
			return
		}
		mu.Lock()
		out := eng.Apply(ents, now(), rooms)
		version, occ := eng.Changed(), lastOcc
		changed := version != lastVersion
		lastVersion = version
		var occBody []byte
		if changed && occ != nil {
			occBody, _ = json.Marshal(eng.ApplyOccupancy(occ))
		}
		mu.Unlock()

		rewritten, _ := json.Marshal(out)
		forward(w, r, rewritten)
		if occBody != nil { // the occupancy counts must follow the people at once
			req, _ := http.NewRequestWithContext(r.Context(), http.MethodPut, upstream+"/api/occupancy", bytes.NewReader(occBody))
			req.Header.Set("Content-Type", "application/json")
			if resp, err := client.Do(req); err == nil {
				resp.Body.Close()
			}
		}
	})
	mux.HandleFunc("PUT /api/occupancy", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
		var occ map[string]any
		if json.Unmarshal(body, &occ) != nil {
			forward(w, r, body)
			return
		}
		mu.Lock()
		lastOcc = occ
		out, _ := json.Marshal(eng.ApplyOccupancy(occ))
		mu.Unlock()
		forward(w, r, out)
	})
	mux.Handle("/", rp)
	return mux
}
