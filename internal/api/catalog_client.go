package api

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"mangahub/gen/catalogpb"
)

// GRPCCatalog calls the separate Catalog Service process. Every call gets its
// own deadline so a stopped or frozen catalog yields 503 instead of a hang.
type GRPCCatalog struct {
	conn    *grpc.ClientConn
	client  catalogpb.CatalogServiceClient
	timeout time.Duration
}

// DialCatalog creates a lazy client: it does not fail if the catalog is down
// at startup; individual calls return ErrCatalogUnavailable instead.
func DialCatalog(addr string, timeout time.Duration) (*GRPCCatalog, error) {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, fmt.Errorf("grpc client %s: %w", addr, err)
	}
	return &GRPCCatalog{conn: conn, client: catalogpb.NewCatalogServiceClient(conn), timeout: timeout}, nil
}

func (g *GRPCCatalog) Close() error { return g.conn.Close() }

func (g *GRPCCatalog) Search(ctx context.Context, query string, limit int) ([]Manga, error) {
	ctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	resp, err := g.client.SearchManga(ctx, &catalogpb.SearchMangaRequest{Query: query, Limit: int32(limit)})
	if err != nil {
		return nil, mapErr(err)
	}
	out := make([]Manga, 0, len(resp.GetItems()))
	for _, m := range resp.GetItems() {
		out = append(out, fromPB(m))
	}
	return out, nil
}

func (g *GRPCCatalog) Get(ctx context.Context, id string) (Manga, error) {
	ctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	m, err := g.client.GetManga(ctx, &catalogpb.GetMangaRequest{Id: id})
	if err != nil {
		return Manga{}, mapErr(err)
	}
	return fromPB(m), nil
}

func (g *GRPCCatalog) UpdateChapters(ctx context.Context, id string, total int) (Manga, error) {
	ctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()
	m, err := g.client.UpdateChapters(ctx, &catalogpb.UpdateChaptersRequest{Id: id, TotalChapters: int32(total)})
	if err != nil {
		return Manga{}, mapErr(err)
	}
	return fromPB(m), nil
}

func fromPB(m *catalogpb.Manga) Manga {
	genres := m.GetGenres()
	if genres == nil {
		genres = []string{}
	}
	return Manga{
		ID:            m.GetId(),
		Title:         m.GetTitle(),
		Author:        m.GetAuthor(),
		Genres:        genres,
		Status:        m.GetStatus(),
		TotalChapters: int(m.GetTotalChapters()),
		Description:   m.GetDescription(),
	}
}

// mapErr converts a gRPC status into one of the package's catalog errors,
// keeping the server's message for the HTTP response.
func mapErr(err error) error {
	st, ok := status.FromError(err)
	if !ok {
		return fmt.Errorf("%w: %v", ErrCatalogUnavailable, err)
	}
	switch st.Code() {
	case codes.NotFound:
		return fmt.Errorf("%w: %s", ErrMangaNotFound, st.Message())
	case codes.InvalidArgument:
		return fmt.Errorf("%w: %s", ErrInvalidArgument, st.Message())
	case codes.Unavailable, codes.DeadlineExceeded, codes.Canceled:
		return fmt.Errorf("%w: %s", ErrCatalogUnavailable, st.Code())
	default:
		return fmt.Errorf("%w: %s: %s", ErrCatalogInternal, st.Code(), st.Message())
	}
}
