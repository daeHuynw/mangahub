package catalog

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"mangahub/internal/api"
)

// Contract test: member A's real gRPC client (internal/api.GRPCCatalog, used
// by the Gin backend) against this real server over a real TCP port. If either
// side changes the proto or the error mapping, this test fails before the demo
// does. internal/api is only imported here, never modified.
func TestContract_BackendClientAgainstRealServer(t *testing.T) {
	repo, _ := newSeededRepo(t)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := NewGRPCServer(repo)
	go srv.Serve(lis)
	defer srv.Stop()

	client, err := api.DialCatalog(lis.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx := context.Background()

	items, err := client.Search(ctx, "piece", 0)
	if err != nil || len(items) != 1 || items[0].ID != "one-piece" {
		t.Fatalf("Search = %v, %v", items, err)
	}
	none, err := client.Search(ctx, "zzzzzz", 0)
	if err != nil || none == nil || len(none) != 0 {
		t.Fatalf("empty Search = %#v, %v; want [] (JSON [] for the TUI)", none, err)
	}

	m, err := client.Get(ctx, "one-piece")
	if err != nil || m.TotalChapters != 1130 || len(m.Genres) == 0 {
		t.Fatalf("Get = %+v, %v", m, err)
	}
	if _, err := client.Get(ctx, "does-not-exist"); !errors.Is(err, api.ErrMangaNotFound) {
		t.Fatalf("unknown id: %v, want api.ErrMangaNotFound (HTTP 404)", err)
	}
	if _, err := client.Get(ctx, ""); !errors.Is(err, api.ErrInvalidArgument) {
		t.Fatalf("empty id: %v, want api.ErrInvalidArgument (HTTP 400)", err)
	}

	m, err = client.UpdateChapters(ctx, "one-piece", 1131)
	if err != nil || m.TotalChapters != 1131 {
		t.Fatalf("UpdateChapters = %+v, %v", m, err)
	}
	if _, err := client.UpdateChapters(ctx, "one-piece", 1131); !errors.Is(err, api.ErrInvalidArgument) {
		t.Fatalf("equal release: %v, want api.ErrInvalidArgument (HTTP 400)", err)
	}
	if _, err := client.UpdateChapters(ctx, "nope-nope", 5); !errors.Is(err, api.ErrMangaNotFound) {
		t.Fatalf("unknown release: %v, want api.ErrMangaNotFound (HTTP 404)", err)
	}

	// Catalog stopped → the backend sees "unavailable" (HTTP 503), quickly.
	srv.Stop()
	start := time.Now()
	if _, err := client.Get(ctx, "one-piece"); !errors.Is(err, api.ErrCatalogUnavailable) {
		t.Fatalf("after stop: %v, want api.ErrCatalogUnavailable (HTTP 503)", err)
	}
	if d := time.Since(start); d > 2500*time.Millisecond {
		t.Fatalf("unavailable error took %s, deadline is 2s", d)
	}
}
