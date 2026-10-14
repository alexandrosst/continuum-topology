package link

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// Handler serves GET /metrics in the Prometheus text format.
func (m *Meter) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+PathPeers, m.peersHandler())
	mux.HandleFunc("POST "+PathIperfServer, m.iperfServerHandler())
	mux.HandleFunc("GET /metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		m.Write(w)
	})
	return mux
}

// Serve listens on addr (for example ":9104") and serves /metrics until the returned function is called. The cluster's
// telemetry collector scrapes it; nothing else is served.
func Serve(addr string, m *Meter, log *slog.Logger) (stop func(), err error) {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	srv := &http.Server{Handler: m.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 4 << 10}
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("link metrics endpoint stopped", "err", err)
		}
	}()
	log.Info("serving link metrics", "addr", ln.Addr().String())
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
	}, nil
}
