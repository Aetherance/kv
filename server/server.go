package server

import (
	"context"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/Aetherance/kv/common"
	"github.com/Aetherance/kv/protocol"
)

// Duck Type
type Coordinator interface {
	Coordinate(ctx context.Context, req *common.Request) *common.Response
}

type Server struct {
	coord     Coordinator
	exporters []exporterEntry

	ctx    context.Context
	cancel context.CancelFunc

	wg sync.WaitGroup
	mu sync.Mutex

	started bool
}

type Option func(*Server)

type exporterEntry struct {
	exporter protocol.Exporter
	addr     string
}

func New(opts ...Option) (*Server, error) {
	ctx, cancel := context.WithCancel(context.Background())

	s := &Server{
		exporters: make([]exporterEntry, 0),
		ctx:       ctx,
		cancel:    cancel,
	}

	for _, opt := range opts {
		opt(s)
	}

	return s, nil
}

func (s *Server) Run() error {
	if s.started {
		return common.ErrServerAlreadyStarted
	}
	s.started = true

	log.Printf("Server is starting with %d protocol exporter.\n", len(s.exporters))

	for _, entry := range s.exporters {
		s.wg.Add(1)
		go func(e exporterEntry) {
			defer s.wg.Done()
			err := e.exporter.Export(s.ctx, s.coord, e.addr)
			if err != nil {
				panic("Export failed for " + " " + e.addr + "!")
			}
		}(entry)
	}

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	<-quit

	log.Println("Gracefully shutdown ...")

	return s.Stop()
}

func WithCoordinator(c Coordinator) Option {
	return func(s *Server) {
		s.coord = c
	}
}

func WithExporter(exp protocol.Exporter, addr string) Option {
	return func(s *Server) {
		s.exporters = append(s.exporters, exporterEntry{
			exporter: exp,
			addr:     addr,
		})
	}
}

func (s *Server) Stop() error {
	if !s.started {
		return common.ErrServerStopWhileServerDidNotRun
	}

	s.cancel()
	s.wg.Wait()

	s.started = false

	log.Println("Server stopped gracefully.")
	return nil
}
