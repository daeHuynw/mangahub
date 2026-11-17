package catalog

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"mangahub/gen/catalogpb"
)

// dialBuf serves store through the real NewGRPCServer (interceptors included)
// on an in-memory bufconn listener and returns a connected client. Everything
// above the socket is real gRPC: HTTP/2 framing, protobuf encoding, status
// codes, deadlines.
func dialBuf(t *testing.T, store Store) (catalogpb.CatalogServiceClient, *grpc.ClientConn) {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	srv := NewGRPCServer(store)
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return catalogpb.NewCatalogServiceClient(conn), conn
}

func ctxT(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// wantCode asserts err is a gRPC status with the given code.
func wantCode(t *testing.T, err error, want codes.Code) *status.Status {
	t.Helper()
	st, ok := status.FromError(err)
	if !ok || st.Code() != want {
		t.Fatalf("err = %v, want code %s", err, want)
	}
	return st
}

func TestRPC_SearchManga(t *testing.T) {
	repo, _ := newSeededRepo(t)
	c, _ := dialBuf(t, repo)
	ctx := ctxT(t)
	total, _ := repo.Count(ctx)

	resp, err := c.SearchManga(ctx, &catalogpb.SearchMangaRequest{Query: "piece"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Items) != 1 || resp.Items[0].GetId() != "one-piece" {
		t.Fatalf("search piece = %v", resp.Items)
	}
	m := resp.Items[0]
	if m.GetTitle() != "One Piece" || m.GetAuthor() == "" || len(m.GetGenres()) == 0 ||
		m.GetStatus() != "ongoing" || m.GetTotalChapters() != 1130 || m.GetDescription() == "" {
		t.Fatalf("fields not carried over gRPC: %v", m)
	}

	all, err := c.SearchManga(ctx, &catalogpb.SearchMangaRequest{})
	if err != nil || len(all.Items) != total {
		t.Fatalf("empty query: %d items, %v; want %d", len(all.GetItems()), err, total)
	}
	limited, err := c.SearchManga(ctx, &catalogpb.SearchMangaRequest{Limit: 3})
	if err != nil || len(limited.Items) != 3 {
		t.Fatalf("limit 3: %d items, %v", len(limited.GetItems()), err)
	}
	none, err := c.SearchManga(ctx, &catalogpb.SearchMangaRequest{Query: "zzzzzz"})
	if err != nil || len(none.Items) != 0 {
		t.Fatalf("no match: %v, %v; want OK with 0 items", none.GetItems(), err)
	}
}

func TestRPC_GetManga(t *testing.T) {
	repo, _ := newSeededRepo(t)
	c, _ := dialBuf(t, repo)
	ctx := ctxT(t)

	m, err := c.GetManga(ctx, &catalogpb.GetMangaRequest{Id: "naruto"})
	if err != nil || m.GetTitle() != "Naruto" || m.GetTotalChapters() != 700 {
		t.Fatalf("get naruto = %v, %v", m, err)
	}

	_, err = c.GetManga(ctx, &catalogpb.GetMangaRequest{Id: ""})
	if st := wantCode(t, err, codes.InvalidArgument); st.Message() != "id is required" {
		t.Fatalf("message = %q (sentinel prefix should be stripped)", st.Message())
	}
	_, err = c.GetManga(ctx, &catalogpb.GetMangaRequest{Id: "does-not-exist"})
	if st := wantCode(t, err, codes.NotFound); st.Message() != `manga not found: "does-not-exist"` {
		t.Fatalf("message = %q", st.Message())
	}
}

func TestRPC_UpdateChapters(t *testing.T) {
	repo, _ := newSeededRepo(t)
	c, _ := dialBuf(t, repo)
	ctx := ctxT(t)

	m, err := c.UpdateChapters(ctx, &catalogpb.UpdateChaptersRequest{Id: "one-piece", TotalChapters: 1131})
	if err != nil || m.GetTotalChapters() != 1131 || m.GetTitle() != "One Piece" {
		t.Fatalf("release = %v, %v", m, err)
	}
	if got, _ := c.GetManga(ctx, &catalogpb.GetMangaRequest{Id: "one-piece"}); got.GetTotalChapters() != 1131 {
		t.Fatalf("GetManga after release = %d, want 1131", got.GetTotalChapters())
	}

	cases := []struct {
		name  string
		id    string
		total int32
		want  codes.Code
	}{
		{"equal", "one-piece", 1131, codes.InvalidArgument},
		{"decrease", "one-piece", 1, codes.InvalidArgument},
		{"empty id", "", 5, codes.InvalidArgument},
		{"unknown id", "does-not-exist", 5, codes.NotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := c.UpdateChapters(ctx, &catalogpb.UpdateChaptersRequest{Id: tc.id, TotalChapters: tc.total})
			st := wantCode(t, err, tc.want)
			if strings.HasPrefix(st.Message(), "invalid argument") {
				t.Fatalf("message %q still has the Go sentinel prefix", st.Message())
			}
		})
	}
}

func TestRPC_HealthService(t *testing.T) {
	repo, _ := newSeededRepo(t)
	_, conn := dialBuf(t, repo)
	resp, err := healthpb.NewHealthClient(conn).Check(ctxT(t), &healthpb.HealthCheckRequest{})
	if err != nil || resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("health = %v, %v", resp, err)
	}
}

// ---------------------------------------------------------------- failures

// fakeStore lets a test choose how every call behaves.
type fakeStore struct {
	fn func(ctx context.Context) (Manga, error)
}

func (f fakeStore) Search(ctx context.Context, _ string, _ int) ([]Manga, error) {
	m, err := f.fn(ctx)
	if err != nil {
		return nil, err
	}
	return []Manga{m}, nil
}
func (f fakeStore) Get(ctx context.Context, _ string) (Manga, error) { return f.fn(ctx) }
func (f fakeStore) UpdateChapters(ctx context.Context, _ string, _ int) (Manga, error) {
	return f.fn(ctx)
}

func TestRPC_DatabaseErrorIsInternalWithoutDetails(t *testing.T) {
	secret := errors.New(`sqlite: disk I/O error at C:\secret\catalog.db: SELECT m.id FROM manga`)
	c, _ := dialBuf(t, fakeStore{fn: func(context.Context) (Manga, error) { return Manga{}, secret }})
	ctx := ctxT(t)

	for name, call := range map[string]func() error{
		"search": func() error { _, err := c.SearchManga(ctx, &catalogpb.SearchMangaRequest{}); return err },
		"get":    func() error { _, err := c.GetManga(ctx, &catalogpb.GetMangaRequest{Id: "x"}); return err },
		"update": func() error {
			_, err := c.UpdateChapters(ctx, &catalogpb.UpdateChaptersRequest{Id: "x", TotalChapters: 2})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			st := wantCode(t, call(), codes.Internal)
			if st.Message() != "internal error" {
				t.Fatalf("message %q leaks internal details", st.Message())
			}
		})
	}
}

func TestRPC_PanicIsRecovered(t *testing.T) {
	calls := 0
	c, _ := dialBuf(t, fakeStore{fn: func(context.Context) (Manga, error) {
		calls++
		if calls == 1 {
			panic("boom")
		}
		return Manga{ID: "ok", Title: "OK", Genres: []string{}}, nil
	}})
	ctx := ctxT(t)

	_, err := c.GetManga(ctx, &catalogpb.GetMangaRequest{Id: "x"})
	wantCode(t, err, codes.Internal)
	// The process survived: the next RPC on the same server works.
	if m, err := c.GetManga(ctx, &catalogpb.GetMangaRequest{Id: "x"}); err != nil || m.GetId() != "ok" {
		t.Fatalf("after panic: %v, %v", m, err)
	}
}

// The client's deadline travels to the server inside the gRPC request
// (grpc-timeout header), so a slow query is abandoned on both sides.
func TestRPC_ClientDeadlineReachesServer(t *testing.T) {
	sawCancel := make(chan struct{}, 1)
	c, _ := dialBuf(t, fakeStore{fn: func(ctx context.Context) (Manga, error) {
		<-ctx.Done() // a query that never finishes on its own
		sawCancel <- struct{}{}
		return Manga{}, ctx.Err()
	}})

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.GetManga(ctx, &catalogpb.GetMangaRequest{Id: "x"})
	wantCode(t, err, codes.DeadlineExceeded)
	if d := time.Since(start); d > time.Second {
		t.Fatalf("took %s, deadline not enforced", d)
	}
	select {
	case <-sawCancel:
	case <-time.After(2 * time.Second):
		t.Fatal("server handler never saw the context cancelled")
	}
}

// GracefulStop lets an in-flight RPC finish, then refuses new work.
func TestGracefulStop_FinishesInFlightRPC(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	store := fakeStore{fn: func(context.Context) (Manga, error) {
		close(started)
		<-release // still working when shutdown begins
		return Manga{ID: "slow", Genres: []string{}}, nil
	}}

	lis, err := net.Listen("tcp", "127.0.0.1:0") // real TCP, dynamic port
	if err != nil {
		t.Fatal(err)
	}
	srv := NewGRPCServer(store)
	go srv.Serve(lis)
	defer srv.Stop()

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	c := catalogpb.NewCatalogServiceClient(conn)

	result := make(chan error, 1)
	go func() {
		m, err := c.GetManga(context.Background(), &catalogpb.GetMangaRequest{Id: "x"})
		if err == nil && m.GetId() != "slow" {
			err = errors.New("wrong response")
		}
		result <- err
	}()
	<-started

	stopped := make(chan struct{})
	go func() { srv.GracefulStop(); close(stopped) }()

	select {
	case <-stopped:
		t.Fatal("GracefulStop returned while an RPC was still running")
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	if err := <-result; err != nil {
		t.Fatalf("in-flight RPC failed during graceful stop: %v", err)
	}
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("GracefulStop did not return after the RPC finished")
	}
	// New connections are refused now.
	if c2, err := net.DialTimeout("tcp", lis.Addr().String(), 500*time.Millisecond); err == nil {
		c2.Close()
		t.Fatal("listener still accepting after GracefulStop")
	}
}
