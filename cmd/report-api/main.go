package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"t-invest-portfolio/internal/config"
	"t-invest-portfolio/internal/reportapi"
	"t-invest-portfolio/internal/store"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "report-api: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	dsn, err := config.SQLDSNFromEnv()
	if err != nil {
		return err
	}
	addr, err := config.ReportAPIAddrFromEnv()
	if err != nil {
		return err
	}
	if err := validateLoopbackAddr(addr); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	db, err := store.OpenSQLServer(dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("слушать %s: %w", addr, err)
	}
	server := &http.Server{Addr: addr, Handler: reportapi.NewHandler(db), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			_ = server.Close()
			return err
		}
		if err := <-done; !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

func validateLoopbackAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("неверный адрес TINVEST_REPORT_API_ADDR: %w", err)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("TINVEST_REPORT_API_ADDR должен указывать на loopback-адрес")
	}
	return nil
}
