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
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		host, port = "", "8080"
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/healthz"
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
