package api

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"mangahub/gen/catalogpb"
)

// stubServer is a tiny real gRPC server used to exercise GRPCCatalog over the
// network (status mapping + deadlines). The real one lives in internal/catalog.
type stubServer struct {
	catalogpb.UnimplementedCatalogServiceServer
	delay time.Duration // simulates a frozen catalog (kill -STOP)
}

func (s *stubServer) wait(ctx context.Context) error {
	if s.delay == 0 {
		return nil
	}
	select {
	case <-time.After(s.delay):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *stubServer) SearchManga(ctx context.Context, r *catalogpb.SearchMangaRequest) (*catalogpb.SearchMangaResponse, error) {
	if err := s.wait(ctx); err != nil {
		return nil, err
	}
	return &catalogpb.SearchMangaResponse{Items: []*catalogpb.Manga{{Id: "one-piece", Title: "One Piece", TotalChapters: 1130}}}, nil
}

func (s *stubServer) GetManga(ctx context.Context, r *catalogpb.GetMangaRequest) (*catalogpb.Manga, error) {
	if err := s.wait(ctx); err != nil {
		return nil, err
	}
	switch r.GetId() {
	case "":
		return nil, status.Error(codes.InvalidArgument, "id is required")
	case "one-piece":
		return &catalogpb.Manga{Id: "one-piece", Title: "One Piece", TotalChapters: 1130}, nil
	}
	return nil, status.Errorf(codes.NotFound, "manga %q not found", r.GetId())
}

func (s *stubServer) UpdateChapters(ctx context.Context, r *catalogpb.UpdateChaptersRequest) (*catalogpb.Manga, error) {
	if r.GetTotalChapters() < 1130 {
		return nil, status.Error(codes.InvalidArgument, "total_chapters cannot decrease")
	}
	return &catalogpb.Manga{Id: r.GetId(), TotalChapters: r.GetTotalChapters()}, nil
}

func startStub(t *testing.T, s *stubServer) (addr string, stop func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gs := grpc.NewServer()
	catalogpb.RegisterCatalogServiceServer(gs, s)
	go gs.Serve(ln)
	t.Cleanup(gs.Stop)
	return ln.Addr().String(), gs.Stop
}

func dialT(t *testing.T, addr string, timeout time.Duration) *GRPCCatalog {
	t.Helper()
	c, err := DialCatalog(addr, timeout)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestGRPCCatalogStatusMapping(t *testing.T) {
	addr, _ := startStub(t, &stubServer{})
	c := dialT(t, addr, 2*time.Second)
	ctx := context.Background()

	if m, err := c.Get(ctx, "one-piece"); err != nil || m.Title != "One Piece" || m.Genres == nil {
		t.Fatalf("Get ok: %+v %v", m, err)
	}
	if _, err := c.Get(ctx, "ghost"); !errors.Is(err, ErrMangaNotFound) || httpStatus(err) != 404 {
		t.Fatalf("NotFound → %v", err)
	}
	if _, err := c.Get(ctx, ""); !errors.Is(err, ErrInvalidArgument) || httpStatus(err) != 400 {
		t.Fatalf("InvalidArgument → %v", err)
	}
	if _, err := c.UpdateChapters(ctx, "one-piece", 10); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("UpdateChapters decrease → %v", err)
	}
	if items, err := c.Search(ctx, "piece", 0); err != nil || len(items) != 1 {
		t.Fatalf("Search: %v %v", items, err)
	}
}

// Catalog process stopped → 503 quickly (connection refused), no hang.
func TestGRPCCatalogStopped(t *testing.T) {
	addr, stop := startStub(t, &stubServer{})
	c := dialT(t, addr, 2*time.Second)
	if _, err := c.Get(context.Background(), "one-piece"); err != nil {
		t.Fatal(err)
	}
	stop()

	start := time.Now()
	_, err := c.Get(context.Background(), "one-piece")
	el := time.Since(start)
	if !errors.Is(err, ErrCatalogUnavailable) || httpStatus(err) != 503 {
		t.Fatalf("stopped catalog → %v", err)
	}
	if el > 2500*time.Millisecond {
		t.Fatalf("took %v, must be bounded by the 2s deadline", el)
	}
	t.Logf("FAULT-TIMING stopped catalog → 503 in %v", el.Round(time.Millisecond))
}

// Catalog frozen (accepts but never answers, like kill -STOP) → deadline → 503 after ~timeout.
func TestGRPCCatalogFrozenHitsDeadline(t *testing.T) {
	addr, _ := startStub(t, &stubServer{delay: time.Minute})
	timeout := 2 * time.Second
	if testing.Short() {
		timeout = 300 * time.Millisecond
	}
	c := dialT(t, addr, timeout)

	start := time.Now()
	_, err := c.Get(context.Background(), "one-piece")
	el := time.Since(start)
	if !errors.Is(err, ErrCatalogUnavailable) {
		t.Fatalf("frozen catalog → %v", err)
	}
	if el < timeout || el > timeout+time.Second {
		t.Fatalf("returned after %v, want ≈ %v", el, timeout)
	}
	t.Logf("FAULT-TIMING frozen catalog (deadline %v) → 503 in %v", timeout, el.Round(time.Millisecond))
}

// Catalog not running at all at startup: lazy client, still 503 not crash.
func TestGRPCCatalogNeverStarted(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close() // nothing listens here
	c := dialT(t, addr, 2*time.Second)
	start := time.Now()
	if _, err := c.Search(context.Background(), "", 0); !errors.Is(err, ErrCatalogUnavailable) {
		t.Fatalf("got %v", err)
	}
	t.Logf("FAULT-TIMING catalog never started → 503 in %v", time.Since(start).Round(time.Millisecond))
}
