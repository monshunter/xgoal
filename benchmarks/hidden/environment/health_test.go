package environment

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthIsReadyAndRepeatable(t *testing.T) {
	for range 2 {
		response := httptest.NewRecorder()
		Health(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		if response.Code != http.StatusOK || response.Body.String() != "ok" {
			t.Fatalf("health = %d %q", response.Code, response.Body.String())
		}
	}
}
