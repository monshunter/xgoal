package environment

import "net/http"

func Health(writer http.ResponseWriter, _ *http.Request) {
	writer.WriteHeader(http.StatusServiceUnavailable)
	_, _ = writer.Write([]byte("starting"))
}
