package jim

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"time"
)

// Server is a lightweight HTTP server scaffold for package jim used by the
// interactive code generator tests and manual prompts.
type Server struct {
	Addr string
	srv  *http.Server
}

// NewServer creates a Server bound to the provided address (e.g. ":8080").
func NewServer(addr string) *Server {
	return &Server{Addr: addr}
}

// Start runs the HTTP server and blocks until an interrupt signal is received.
// It returns any error encountered during graceful shutdown.
func (s *Server) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleRoot)
	mux.HandleFunc("/health", s.handleHealth)

	s.srv = &http.Server{
		Addr:    s.Addr,
		Handler: mux,
	}

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)

	go func() {
		log.Printf("jim: starting server on %s", s.Addr)
		if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("jim: server error: %v", err)
		}
	}()

	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.srv.Shutdown(ctx)
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	fmt.Fprintln(w, "hello from jim server")
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, "ok")
}
func StartServer() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.
			Fprintln(w, "hello from StartServer")
	})
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Println(err)
	}
}
func StartServer2() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.
			Fprintln(w,

				"hello from StartServer2",
			)
	})
	if err := http.ListenAndServe(":8080", nil); err !=
		nil {
		log.Println(err)
	}
}
