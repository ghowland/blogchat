package main

import (
	"context"
	"crypto/rand"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	confPath := flag.String("config", "config.json", "path to the configuration file")
	seedMail := flag.String("seed-email", "", "email address of the first member")
	seedName := flag.String("seed-handle", "", "handle of the first member")
	flag.Parse()

	log.SetFlags(log.LstdFlags | log.LUTC)

	conf, err := LoadConfig(*confPath)
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	app := &App{
		limits: NewLimiter(),
		seen:   NewSeenCache(60 * time.Second),
		secret: make([]byte, 32),
		start:  time.Now(),
	}
	if _, err := rand.Read(app.secret); err != nil {
		log.Fatalf("secret: %v", err)
	}
	app.conf.Store(conf)

	table, err := LoadGeo(conf)
	if err != nil {
		log.Fatalf("geo: %v", err)
	}
	app.geo.Store(table)

	app.dbh, err = OpenDB(conf.DBPath)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer app.dbh.Close()

	app.views, err = LoadViews()
	if err != nil {
		log.Fatalf("templates: %v", err)
	}

	if err := app.SeedFirstUser(*seedMail, *seedName); err != nil {
		log.Fatalf("seed: %v", err)
	}

	go app.purgeLoop()

	// Turn 2 replaces the placeholder with app.Routes().
	srv := &http.Server{
		Addr:              conf.Listen,
		Handler:           app.GeoBlock(app.Routes()),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          log.Default(),
	}

	go func() {
		log.Printf("listen on %s", conf.Listen)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	app.waitForSignal(srv, *confPath)
}

// waitForSignal reloads on SIGHUP and stops on SIGINT or SIGTERM.
func (app *App) waitForSignal(srv *http.Server, confPath string) {
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)

	for sig := range sigs {
		if sig == syscall.SIGHUP {
			if err := app.Reload(confPath); err != nil {
				log.Printf("reload failed, the old configuration stays active: %v", err)
			} else {
				log.Printf("configuration reloaded")
			}
			continue
		}

		log.Printf("shutdown after %v", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("shutdown: %v", err)
		}
		cancel()

		if err := Checkpoint(app.dbh); err != nil {
			log.Printf("checkpoint: %v", err)
		}
		if err := app.dbh.Close(); err != nil {
			log.Printf("close database: %v", err)
		}
		return
	}
}

// purgeLoop removes expired sessions and expired login tokens each hour.
func (app *App) purgeLoop() {
	for range time.Tick(time.Hour) {
		if err := app.PurgeExpired(); err != nil {
			log.Printf("purge: %v", err)
		}
	}
}

// placeholderRouter exists only in turn 1. Turn 2 deletes this function.
func placeholderRouter() http.Handler {
	return http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		http.Error(res, "router not built yet", http.StatusNotImplemented)
	})
}
