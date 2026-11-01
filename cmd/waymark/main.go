package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // any site.timezone, even without the OS zone files

	"github.com/Will-Luck/waymark/internal/app"
	"github.com/Will-Luck/waymark/internal/config"
	"github.com/Will-Luck/waymark/internal/demo"
	"github.com/Will-Luck/waymark/internal/model"
	"github.com/Will-Luck/waymark/internal/web"
)

var version = "dev"

func main() {
	fixture := flag.String("fixture", "", "serve a fixed Site JSON file (no network)")
	listen := flag.String("listen", "", "listen address (default: the config's, :8080)")
	dump := flag.String("dump", "", "collect once, write the assembled Site JSON to this path and exit")
	demoMode := flag.Bool("demo", false, "serve an invented team's projects from a built-in fake forge (no config, no network)")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("waymark " + version)
		return
	}

	var cfg config.Config
	if *demoMode {
		dir, err := os.MkdirTemp("", "waymark-demo-")
		if err != nil {
			log.Fatal(err)
		}
		defer os.RemoveAll(dir)
		d, err := demo.Run(demo.Dataset(time.Now()), dir, demo.FeedEvery)
		if err != nil {
			log.Fatal(err)
		}
		defer d.Close()
		cfg = d.Config
		log.Printf("demo: fake Gitea at %s, fake GitHub at %s, sites at %s, files in %s", d.GiteaURL, d.GitHubURL, d.SitesURL, dir)
	} else {
		path := config.ResolvePath(os.Getenv, config.FileExists)
		log.Print(configLine(path))
		c, err := config.Load(path, os.Getenv)
		if err != nil {
			log.Fatal(err)
		}
		for _, w := range c.Warnings {
			log.Printf("config: warning: %s", w)
		}
		cfg = c
	}
	cfg.Version = version
	if *listen == "" {
		*listen = cfg.Listen
	}
	if *fixture == "" {
		if err := cfg.Validate(); err != nil {
			log.Fatal(err)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *dump != "" {
		if err := runDump(ctx, cfg, *dump); err != nil {
			log.Fatal(err)
		}
		return
	}

	var src model.Source
	runDone := make(chan struct{})
	if *fixture != "" {
		s, err := model.LoadFixture(*fixture)
		if err != nil {
			log.Fatal(err)
		}
		src = s
		close(runDone)
	} else {
		a, err := app.New(cfg, app.NewForges(cfg))
		if err != nil {
			log.Fatal(err)
		}
		src = a
		go func() {
			defer close(runDone)
			if err := a.Run(ctx); err != nil {
				log.Printf("collectors: %v", err)
			}
		}()
	}

	srv := &http.Server{
		Addr:              *listen,
		Handler:           web.New(src, web.Options{Version: version, Dev: *fixture != ""}),
		ReadHeaderTimeout: 10 * time.Second,
		// Request contexts end on shutdown, so open SSE streams return.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	mode := "live"
	switch {
	case *fixture != "":
		mode = "fixture " + *fixture
	case *demoMode:
		mode = "demo"
	}
	log.Printf("waymark %s listening on %s (%s)", version, *listen, mode)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	<-runDone
}

// configLine says which config is read: the file's path, or that there is
// none (the env-only quick start). Never a value from it.
func configLine(path string) string {
	if path == "" {
		return "config: environment only"
	}
	return "config: " + path
}

// runDump collects once and writes the site atomically (path.tmp, rename).
func runDump(ctx context.Context, cfg config.Config, path string) error {
	a, err := app.New(cfg, app.NewForges(cfg))
	if err != nil {
		return err
	}
	site, err := a.RunOnce(ctx)
	if err != nil {
		return err
	}
	b, err := json.MarshalIndent(site, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	failing := 0
	for _, s := range site.Sources {
		if !s.OK {
			failing++
			log.Printf("source %s: %s", s.Name, s.Error)
		}
	}
	fmt.Fprintf(os.Stderr, "waymark %s: wrote %s: %d projects, %d needs-you, %d sources failing\n", version, path, len(site.Projects), len(site.NeedsYou), failing)
	return nil
}
