package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthcheckURL(t *testing.T) {
	for in, want := range map[string]string{
		":8080":          "http://127.0.0.1:8080/healthz",
		"0.0.0.0:9000":   "http://127.0.0.1:9000/healthz",
		"127.0.0.1:1234": "http://127.0.0.1:1234/healthz",
		"[::]:8081":      "http://127.0.0.1:8081/healthz",
	} {
		if got := healthURL(in); got != want {
			t.Errorf("healthURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestProbe(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer ok.Close()
	if err := probe(ok.URL); err != nil {
		t.Fatal(err)
	}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) }))
	defer bad.Close()
	if err := probe(bad.URL); err == nil {
		t.Fatal("expected error")
	}
}
