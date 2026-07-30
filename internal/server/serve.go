package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	readHeaderTimeout = 10 * time.Second
	readTimeout       = 5 * time.Minute
	writeTimeout      = 5 * time.Minute
	idleTimeout       = 5 * time.Minute
	shutdownTimeout   = 30 * time.Second
)

func serve(ctx context.Context, active runtime) error {
	return serveWithHTTPShutdownTimeout(ctx, active, shutdownTimeout)
}

func serveWithHTTPShutdownTimeout(ctx context.Context, active runtime, timeout time.Duration) error {
	return serveWithHandlerGate(ctx, active, timeout, newHandlerGate(nil))
}

func serveWithHandlerGate(ctx context.Context, active runtime, timeout time.Duration, handlers *handlerGate) (err error) {
	defer func() {
		err = errors.Join(err, closeRuntime(active))
	}()

	// serverCtx owns every admitted request's context; cancelling it after
	// the HTTP shutdown finishes unblocks handlers still observing
	// r.Context(), so the bounded drain below can complete.
	serverCtx, cancelServerWork := context.WithCancel(ctx)
	defer cancelServerWork()

	server := newHTTPServer(serverCtx, active.handler, active.maxRequestBodyBytes, handlers)
	serveResult := make(chan error, 1)
	go func() {
		serveResult <- server.Serve(active.listener)
	}()

	var serveErr error
	serveStopped := false
	select {
	case serveErr = <-serveResult:
		serveStopped = true
	case <-ctx.Done():
	}
	handlers.closeAdmission()
	deadline := time.Now().Add(timeout)
	shutdownCtx, cancelShutdown := context.WithDeadline(context.Background(), deadline)
	shutdownErr := shutdownHTTP(server, shutdownCtx)
	cancelShutdown()
	cancelServerWork()
	if !serveStopped {
		serveErr = <-serveResult
	}
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	} else if serveErr != nil {
		if serveStopped {
			serveErr = fmt.Errorf("serve Tailnet HTTP: %w", serveErr)
		} else {
			serveErr = fmt.Errorf("serve Tailnet HTTP during shutdown: %w", serveErr)
		}
	}

	// The drain shares the HTTP shutdown deadline, so a handler that ignores
	// its context cannot extend the configured shutdown bound. The waiter
	// channel is buffered so an abandoned waiter goroutine always sends and
	// exits.
	drained := make(chan struct{}, 1)
	go func() {
		handlers.wait()
		drained <- struct{}{}
	}()
	var drainErr error
	select {
	case <-drained:
	default:
		remaining := time.Until(deadline)
		if remaining <= 0 {
			drainErr = fmt.Errorf("handler drain exceeded %s: abandoning stuck handlers", timeout)
		} else {
			timer := time.NewTimer(remaining)
			defer timer.Stop()
			select {
			case <-drained:
			case <-timer.C:
				drainErr = fmt.Errorf("handler drain exceeded %s: abandoning stuck handlers", timeout)
			}
		}
	}
	return errors.Join(shutdownErr, serveErr, drainErr)
}

func newHTTPServer(baseCtx context.Context, handler http.Handler, maxRequestBodyBytes int64, handlers *handlerGate) *http.Server {
	return &http.Server{
		BaseContext: func(net.Listener) context.Context { return baseCtx },
		Handler: http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			if !handlers.admit() {
				writer.Header().Set("Connection", "close")
				http.Error(writer, "server is shutting down", http.StatusServiceUnavailable)
				return
			}
			defer handlers.done()
			request.Body = http.MaxBytesReader(writer, request.Body, maxRequestBodyBytes)
			handler.ServeHTTP(writer, request)
		}),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
	}
}

type handlerGate struct {
	mu              sync.Mutex
	drained         *sync.Cond
	admissionOpen   bool
	active          int
	beforeAdmission func()
	afterAdmission  func(bool)
}

func newHandlerGate(beforeAdmission func()) *handlerGate {
	gate := &handlerGate{
		admissionOpen:   true,
		beforeAdmission: beforeAdmission,
	}
	gate.drained = sync.NewCond(&gate.mu)
	return gate
}

func (g *handlerGate) admit() bool {
	if g.beforeAdmission != nil {
		g.beforeAdmission()
	}
	g.mu.Lock()
	admitted := g.admissionOpen
	if admitted {
		g.active++
	}
	g.mu.Unlock()
	if g.afterAdmission != nil {
		g.afterAdmission(admitted)
	}
	return admitted
}

func (g *handlerGate) done() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.active--
	if g.active == 0 {
		g.drained.Broadcast()
	}
}

func (g *handlerGate) closeAdmission() {
	g.mu.Lock()
	g.admissionOpen = false
	g.mu.Unlock()
}

func (g *handlerGate) wait() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for g.active != 0 {
		g.drained.Wait()
	}
}

func shutdownHTTP(server *http.Server, ctx context.Context) error {
	if err := server.Shutdown(ctx); err != nil {
		return errors.Join(fmt.Errorf("gracefully shut down Tailnet HTTP: %w", err), server.Close())
	}
	return nil
}
