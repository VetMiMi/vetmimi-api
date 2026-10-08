package main

import (
	"net/http"
	"time"
)

// healthcheck asks the API on this container's port whether it serves and
// returns the exit code. It is the Compose health check, because the
// distroless image has no shell or curl; it reads only PORT, so a broken
// configuration cannot hide behind it.
func healthcheck(port string) int {
	if port == "" {
		port = "8080"
	}
	client := http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://127.0.0.1:" + port + "/healthz")
	if err != nil {
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
