package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"livecal/internal/server"
)

func main() {
	cfg, err := server.LoadConfig()
	if err != nil {
		log.Fatal(err)
	}
	srv, err := server.New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	hs := &http.Server{Addr: cfg.Addr, Handler: srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second}
	go func() {
		log.Printf("Live Calendar listening on %s (%s)", cfg.Addr, cfg.BaseURL)
		if err := hs.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	hs.Shutdown(ctx)
	srv.Close()
}
