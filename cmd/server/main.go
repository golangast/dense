package main

import (
	"fmt"
	"net"
)

func StartServe(addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("StartServe failed: %w", err)
	}
	conn, err := ln.Accept()
	if err != nil {
		return fmt.Errorf("StartServe failed: %w", err)
	}
	_ = conn
	return nil
}

func main() {}
