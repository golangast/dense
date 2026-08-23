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

func NewServer(addr string) *Server {
	return &Server{Addr: addr}
}

// NewServer creates a Server bound to the provided address (e.g. ":8080").

// Start runs the HTTP server and blocks until an interrupt signal is received.
// It returns any error encountered during graceful shutdown.

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
