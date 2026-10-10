package main

import (
	"net/http"
	"time"
)

// healthcheck is the Compose health check, since the distroless image has no
// curl. It reads only PORT, so a broken configuration cannot hide behind it.
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
