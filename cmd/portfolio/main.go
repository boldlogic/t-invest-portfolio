package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"t-invest-portfolio/internal/config"
	"t-invest-portfolio/internal/store"
	"t-invest-portfolio/internal/tinvest"
	"t-invest-portfolio/internal/worker"
)

func main() {
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	dumpPath := flag.String("dump", tinvest.DumpPath, "JSONL ответов API; пустая строка отключает файл")
	flag.Parse()

	cfg, err := config.FromEnv()
	if err != nil {
		log.Fatal(err)
	}
	db, err := store.OpenSQLServer(cfg.SQLDSN)
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			log.Printf("закрытие базы: %v", err)
		}
	}()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	client, err := tinvest.New(ctx, cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := client.Close(); err != nil {
			log.Printf("закрытие клиента: %v", err)
		}
	}()

	dump, err := tinvest.OpenDump(*dumpPath)
	if err != nil {
		log.Fatal(err)
	}
	defer func() {
		if err := dump.Close(); err != nil {
			log.Printf("закрытие дампа: %v", err)
		}
	}()
	client.UseDump(dump)
	if dump != nil {
		log.Printf("лог ответов: %s", *dumpPath)
		client.NoteSession(cfg.Endpoint, cfg.CAFile)
	}

	engine := worker.New(client, db, cfg, log.Default())
	if err := engine.Run(ctx); err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
