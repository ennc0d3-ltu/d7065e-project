package actuator

import (
	"encoding/json"
	"net/http"

	"github.com/ennc0d3-learn/ltu-d705e-edge-intelligence/src/internal/gateway"
)

// ReadingsHandler serves POST /readings: the gateway's direct path to the
// actuator, so the local fallback still has data when the broker is down.
// Readings are also fed by MQTT; a repeat is harmless because the feature
// windows ignore any sample that is not newer than the latest.
func ReadingsHandler(onReading func(gateway.Reading)) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/readings", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var rd gateway.Reading
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&rd); err != nil || rd.Room == "" || rd.Type == "" {
			http.Error(w, "need a JSON reading with room and type", http.StatusBadRequest)
			return
		}
		onReading(rd)
		w.WriteHeader(http.StatusNoContent)
	})
	return mux
}
