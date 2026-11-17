package catalog

import (
	"context"
	"errors"
	"log"
	"runtime/debug"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"

	"mangahub/gen/catalogpb"
)

// Store is what the gRPC layer needs from the data layer. *Repo implements
// it; tests use fakes to simulate database failures, panics and slow queries.
type Store interface {
	Search(ctx context.Context, query string, limit int) ([]Manga, error)
	Get(ctx context.Context, id string) (Manga, error)
	UpdateChapters(ctx context.Context, id string, total int) (Manga, error)
}

// Server implements catalogpb.CatalogServiceServer on top of a Store.
// It only translates: protobuf ↔ Manga, and Go errors → gRPC status codes.
type Server struct {
	catalogpb.UnimplementedCatalogServiceServer // returns Unimplemented for future RPCs
	store                                       Store
}

// NewServer returns the CatalogService implementation backed by store.
func NewServer(store Store) *Server { return &Server{store: store} }

// NewGRPCServer builds a ready-to-serve *grpc.Server: logging and panic
// recovery interceptors, the CatalogService, the standard health service and
// server reflection (so `grpcurl -plaintext localhost:9092 list` works in the
// demo). cmd/catalog and the tests use exactly the same server.
func NewGRPCServer(store Store, opts ...grpc.ServerOption) *grpc.Server {
	opts = append([]grpc.ServerOption{
		// Order: logging is outermost so it also records the Internal status
		// produced when recovery catches a panic.
		grpc.ChainUnaryInterceptor(loggingInterceptor, recoveryInterceptor),
	}, opts...)
	s := grpc.NewServer(opts...)
	catalogpb.RegisterCatalogServiceServer(s, NewServer(store))
	healthpb.RegisterHealthServer(s, health.NewServer()) // reports SERVING for ""
	reflection.Register(s)
	return s
}

// SearchManga lists manga matching query. An empty result is a normal answer
// (items: []), never an error.
func (s *Server) SearchManga(ctx context.Context, req *catalogpb.SearchMangaRequest) (*catalogpb.SearchMangaResponse, error) {
	items, err := s.store.Search(ctx, req.GetQuery(), int(req.GetLimit()))
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	resp := &catalogpb.SearchMangaResponse{Items: make([]*catalogpb.Manga, 0, len(items))}
	for _, m := range items {
		resp.Items = append(resp.Items, toPB(m))
	}
	return resp, nil
}

// GetManga returns one manga. Errors: InvalidArgument (empty id), NotFound.
func (s *Server) GetManga(ctx context.Context, req *catalogpb.GetMangaRequest) (*catalogpb.Manga, error) {
	m, err := s.store.Get(ctx, req.GetId())
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	return toPB(m), nil
}

// UpdateChapters records a chapter release and returns the updated manga.
// Errors: NotFound, InvalidArgument (empty id or total <= current).
func (s *Server) UpdateChapters(ctx context.Context, req *catalogpb.UpdateChaptersRequest) (*catalogpb.Manga, error) {
	m, err := s.store.UpdateChapters(ctx, req.GetId(), int(req.GetTotalChapters()))
	if err != nil {
		return nil, toStatus(ctx, err)
	}
	log.Printf("catalog: %s total_chapters -> %d", m.ID, m.TotalChapters)
	return toPB(m), nil
}

func toPB(m Manga) *catalogpb.Manga {
	return &catalogpb.Manga{
		Id:            m.ID,
		Title:         m.Title,
		Author:        m.Author,
		Genres:        m.Genres,
		Status:        m.Status,
		TotalChapters: int32(m.TotalChapters),
		Description:   m.Description,
	}
}

// toStatus maps a data-layer error to a gRPC status (contracts.md §7).
//
//   - ErrNotFound: `manga not found: "x"` (the backend replaces it with its
//     own fixed "manga not found" text anyway).
//   - ErrInvalidArgument: the backend shows this message to the user as is,
//     so the Go sentinel prefix is stripped: "total_chapters must be greater
//     than current value 1131 (got 1)".
//   - The caller gave up (deadline / cancel): report that, not Internal.
//   - Anything else (SQL error, disk full, ...): logged in full here, but the
//     client only gets "internal error" so file paths and SQL never leak.
func toStatus(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return status.Error(codes.NotFound, err.Error()) // `manga not found: "x"`
	case errors.Is(err, ErrInvalidArgument):
		return status.Error(codes.InvalidArgument, humanMessage(err, ErrInvalidArgument))
	case ctx.Err() != nil:
		return status.FromContextError(ctx.Err()).Err()
	default:
		log.Printf("catalog: internal error: %v", err)
		return status.Error(codes.Internal, "internal error")
	}
}

func humanMessage(err, sentinel error) string {
	msg := strings.TrimPrefix(err.Error(), sentinel.Error()+": ")
	if msg == "" {
		return sentinel.Error()
	}
	return msg
}

// loggingInterceptor writes one line per RPC: method, request, status code
// and duration, e.g.
//
//	rpc GetManga {id:"one-piece"} -> OK (1.2ms)
func loggingInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	start := time.Now()
	resp, err := handler(ctx, req)
	method := info.FullMethod[strings.LastIndex(info.FullMethod, "/")+1:]
	st := status.Convert(err)
	if err != nil {
		log.Printf("rpc %s {%v} -> %s: %s (%s)", method, req, st.Code(), st.Message(), time.Since(start).Round(time.Microsecond))
	} else {
		log.Printf("rpc %s {%v} -> OK (%s)", method, req, time.Since(start).Round(time.Microsecond))
	}
	return resp, err
}

// recoveryInterceptor turns a panic in a handler into codes.Internal, so one
// bad request cannot crash the whole catalog process.
func recoveryInterceptor(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("catalog: panic in %s: %v\n%s", info.FullMethod, r, debug.Stack())
			resp, err = nil, status.Error(codes.Internal, "internal error")
		}
	}()
	return handler(ctx, req)
}
