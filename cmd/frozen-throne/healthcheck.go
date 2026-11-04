package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"time"
)

// healthURL is the loopback /healthz URL for a listen address.
func healthURL(listen string) string {
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		port = "8080"
	}
	return "http://" + net.JoinHostPort("127.0.0.1", port) + "/healthz"
}

func probe(url string) error {
	resp, err := (&http.Client{Timeout: 3 * time.Second}).Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz returned %s", resp.Status)
	}
	return nil
}

// healthcheck is used by the container HEALTHCHECK: the runtime image has no
// shell or curl.
func healthcheck() error {
	addr := os.Getenv("FT_LISTEN_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	return probe(healthURL(addr))
}
