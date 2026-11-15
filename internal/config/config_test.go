package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	for _, k := range []string{"HTTP_ADDR", "TCP_ADDR", "UDP_ADDR", "CATALOG_ADDR", "BACKEND_DB", "CATALOG_TIMEOUT"} {
		t.Setenv(k, "")
	}
	c := Load()
	if c.HTTPAddr != ":8080" || c.TCPAddr != ":9090" || c.UDPAddr != ":9091" || c.CatalogAddr != "localhost:9092" {
		t.Fatalf("unexpected defaults: %+v", c)
	}
	if c.CatalogTimeout != 2*time.Second {
		t.Fatalf("catalog timeout = %v", c.CatalogTimeout)
	}
}

func TestLoadOverride(t *testing.T) {
	t.Setenv("HTTP_ADDR", ":8099")
	t.Setenv("CATALOG_TIMEOUT", "500ms")
	c := Load()
	if c.HTTPAddr != ":8099" {
		t.Fatalf("HTTPAddr = %q", c.HTTPAddr)
	}
	if c.CatalogTimeout != 500*time.Millisecond {
		t.Fatalf("CatalogTimeout = %v", c.CatalogTimeout)
	}
}
