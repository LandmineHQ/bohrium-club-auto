package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata"

	"bohrium-club-auto/internal/bohrium"
	"bohrium-club-auto/internal/chromehttp"
	"bohrium-club-auto/internal/config"
	"bohrium-club-auto/internal/prompts"
	"bohrium-club-auto/internal/recon"
	"bohrium-club-auto/internal/scheduler"
	"bohrium-club-auto/internal/server"
	"bohrium-club-auto/internal/store"
)

// Version is overridden at build time via -ldflags "-X main.Version=...".
var Version = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version", "--version", "-v":
			fmt.Printf("bohrium-club-auto %s\n", Version)
			return
		case "recon":
			os.Exit(runReconCLI(os.Args[2:]))
		case "serve":
			runServer()
			return
		case "help", "--help", "-h":
			printUsage()
			return
		}
	}
	runServer()
}

func printUsage() {
	fmt.Printf(`bohrium-club-auto %s — Bohrium User Community Daily Automation & Dashboard

Usage:
  bohrium-club-auto [serve]               Start Web Dashboard & daily scheduler
  bohrium-club-auto recon pull [flags]    Pull live Bohrium frontend snapshot into recon/
  bohrium-club-auto recon diff [flags]    Compare live Bohrium frontend against local recon/ baseline
  bohrium-club-auto version               Print build version

Environment Variables:
  BOHRIUM_HOST                 Listen host (default 0.0.0.0)
  BOHRIUM_PORT                 Listen port (default 8788)
  BOHRIUM_DATA_DIR             Persistent data directory (default data)
  BOHRIUM_ADMIN_PASSWORD       Admin password for Dashboard (auto-generated on first boot if unset)
  BOHRIUM_USER_AGENT           Outgoing Chrome User-Agent override
  BOHRIUM_PROXY                Optional HTTP/HTTPS proxy URL
  BOHRIUM_SCHEDULE_ENABLED     Enable daily auto-run (default true)
  BOHRIUM_SCHEDULE_TIME        Daily run time HH:MM in TZ (default 08:15)
  BOHRIUM_SCHEDULE_TZ          Schedule timezone (default Asia/Shanghai)
  BOHRIUM_GATEWAY_ENABLED      Enable OpenAI /v1/* API gateway (default true)
  BOHRIUM_API_KEYS             Comma-separated API keys for /v1/* (auto-generated if unset)
  BOHRIUM_GATEWAY_DEFAULT_MODEL Default model when omitted (default dspro-high)
  BOHRIUM_GATEWAY_ALLOW_NO_AUTH Allow unauthenticated /v1/* calls (default false)
`, Version)
}

func runServer() {
	cfgMgr, err := config.Load()
	if err != nil {
		log.Fatalf("[boot] failed to load config: %v", err)
	}
	cfg := cfgMgr.Get()

	st, err := store.Open(cfg.DataDir)
	if err != nil {
		log.Fatalf("[boot] failed to open store: %v", err)
	}

	httpCli, err := chromehttp.New(chromehttp.Options{
		UserAgent: cfg.UserAgent,
		ProxyURL:  cfg.ProxyURL,
		Timeout:   45 * time.Second,
	})
	if err != nil {
		log.Fatalf("[boot] failed to init Chrome HTTP client: %v", err)
	}

	client := bohrium.NewClient(httpCli, cfgMgr, st)
	sched := scheduler.New(client, cfgMgr, st)
	sched.Start()

	srv := server.New(Version, cfgMgr, st, httpCli, client, sched)
	addr := net.JoinHostPort(cfg.Host, fmt.Sprintf("%d", cfg.Port))
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 15 * time.Second,
	}

	pStats := prompts.GetStats()
	log.Printf("============================================================")
	log.Printf("bohrium-club-auto %s", Version)
	log.Printf("Dashboard:      http://%s/", displayHost(cfg.Host, cfg.Port))
	if cfg.Gateway.Enabled {
		log.Printf("OpenAI API:     http://%s/v1 (Chat Completions + Responses)", displayHost(cfg.Host, cfg.Port))
	}
	log.Printf("Data Dir:       %s", cfg.DataDir)
	log.Printf("Chrome UA:      %s", httpCli.UserAgent())
	log.Printf("Prompt Corpus:  SciSpace=%d, Writing=%d, Reading=%d, Papers=%d",
		pStats.ScienceSpacePrompts, pStats.AcademicWritingPrompts, pStats.LiteratureReadingPrompts, pStats.PaperCatalogCount)
	if pw := cfgMgr.GeneratedPassword(); pw != "" {
		log.Printf("------------------------------------------------------------")
		log.Printf("FIRST BOOT ADMIN PASSWORD: %s", pw)
		log.Printf("(Saved to %s; override with BOHRIUM_ADMIN_PASSWORD env)", cfgMgr.Path())
		log.Printf("------------------------------------------------------------")
	} else {
		log.Printf("Admin Auth:     enabled (configured via env or %s)", cfgMgr.Path())
	}
	if apiKey := cfgMgr.GeneratedAPIKey(); apiKey != "" {
		log.Printf("------------------------------------------------------------")
		log.Printf("GENERATED OPENAI API KEY: %s", apiKey)
		log.Printf("(Saved to %s; manage in Web Console or set BOHRIUM_API_KEYS)", cfgMgr.Path())
		log.Printf("------------------------------------------------------------")
	} else {
		log.Printf("OpenAI Auth:    %d key(s) configured (allowNoAuth=%v)", len(cfg.Gateway.APIKeys), cfg.Gateway.AllowNoAuth)
	}
	log.Printf("============================================================")

	errCh := make(chan error, 1)
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
		close(errCh)
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		log.Printf("[shutdown] received %s, shutting down gracefully...", sig)
	case err := <-errCh:
		if err != nil {
			log.Fatalf("[server] listen failed: %v", err)
		}
	}

	sched.Stop()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(ctx)
	log.Printf("[shutdown] clean exit")
}

func displayHost(host string, port int) string {
	if host == "" || host == "0.0.0.0" || host == "::" {
		return fmt.Sprintf("127.0.0.1:%d", port)
	}
	return fmt.Sprintf("%s:%d", host, port)
}

func runReconCLI(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Println("Usage: bohrium-club-auto recon <pull|diff> [flags]")
		fmt.Println("  --out <dir>         Snapshot directory (default recon/bohrium-web)")
		fmt.Println("  --manifest <file>   Baseline manifest path (default <out>/manifest.json)")
		fmt.Println("  --targets <urls>    Comma-separated entry URLs (default club+platform)")
		fmt.Println("  --proxy <url>       HTTP/HTTPS proxy")
		fmt.Println("  --no-proxy          Ignore proxy env vars")
		fmt.Println("  --max-assets <n>    Max static assets to crawl (default 200)")
		fmt.Println("  --delay-ms <n>      Delay between requests in ms (default 80)")
		fmt.Println("  --timeout-ms <n>    Per-request timeout in ms (default 25000)")
		fmt.Println("  --tolerant          Continue on individual asset errors")
		fmt.Println("  --json              Emit JSON output (diff/pull)")
		return 0
	}

	sub := args[0]
	fs := flag.NewFlagSet("recon "+sub, flag.ContinueOnError)
	outDir := fs.String("out", recon.DefaultOutDir, "snapshot directory")
	manifestPath := fs.String("manifest", "", "manifest path")
	targetsRaw := fs.String("targets", "", "comma-separated entry URLs")
	proxy := fs.String("proxy", "", "HTTP/HTTPS proxy")
	noProxy := fs.Bool("no-proxy", false, "ignore proxy env")
	maxAssets := fs.Int("max-assets", recon.DefaultMaxAssets, "max assets")
	delayMs := fs.Int("delay-ms", 80, "request delay ms")
	timeoutMs := fs.Int("timeout-ms", 25000, "request timeout ms")
	tolerant := fs.Bool("tolerant", true, "continue on single asset failure")
	asJSON := fs.Bool("json", false, "output JSON")

	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}

	proxyURL := strings.TrimSpace(*proxy)
	if !*noProxy && proxyURL == "" {
		for _, k := range []string{"BOHRIUM_RECON_PROXY", "BOHRIUM_PROXY", "HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
			if v := strings.TrimSpace(os.Getenv(k)); v != "" {
				proxyURL = v
				break
			}
		}
	}

	var targets []string
	if strings.TrimSpace(*targetsRaw) != "" {
		for _, part := range strings.Split(*targetsRaw, ",") {
			if p := strings.TrimSpace(part); p != "" {
				targets = append(targets, p)
			}
		}
	}

	opts := recon.Options{
		TargetURLs:   targets,
		OutDir:       *outDir,
		ManifestPath: *manifestPath,
		UserAgent:    strings.TrimSpace(os.Getenv("BOHRIUM_USER_AGENT")),
		ProxyURL:     proxyURL,
		MaxAssets:    *maxAssets,
		DelayMs:      *delayMs,
		TimeoutMs:    *timeoutMs,
		Tolerant:     *tolerant,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	switch sub {
	case "pull":
		manifest, err := recon.TakeSnapshot(ctx, opts, true)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[recon] pull failed: %v\n", err)
			return 1
		}
		if *asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(manifest)
			return 0
		}
		fmt.Println("[recon] snapshot written")
		fmt.Printf("  dir:      %s\n", manifest.OutDir)
		fmt.Printf("  manifest: %s\n", manifest.ManifestPath)
		fmt.Printf("  targets:  %d\n", len(manifest.Targets))
		fmt.Printf("  assets:   %d (total %d bytes)\n", len(manifest.Assets), manifest.TotalBytes)
		fmt.Printf("  fetched:  %s\n", manifest.FetchedAt)
		fmt.Printf("  markers:  tasks=%v claim=%v gateway=%v wechatQR=%v\n",
			manifest.Markers.PlazaTasksEndpoint,
			manifest.Markers.PlazaClaimEndpoint,
			manifest.Markers.GatewayHeader,
			manifest.Markers.WeChatQREndpoint)
		return 0

	case "diff", "compare":
		diff, err := recon.CompareLive(ctx, opts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[recon] diff failed: %v\n", err)
			return 1
		}
		if *asJSON {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			_ = enc.Encode(diff)
		} else {
			fmt.Println(diff.Summary)
			if diff.BaselineExists {
				fmt.Printf("  baseline: %s (%d assets)\n", diff.BaselineFetchedAt, diff.BaselineAssets)
				fmt.Printf("  current:  %s (%d assets)\n", diff.CurrentFetchedAt, diff.CurrentAssets)
				for _, m := range diff.MarkerChanges {
					fmt.Printf("  ! marker:  %s\n", m)
				}
				for _, a := range diff.Added {
					fmt.Printf("  + added:   %s\n", a.URL)
				}
				for _, c := range diff.Changed {
					fmt.Printf("  ~ changed: %s (%s -> %s)\n", c.URL, shortHash(c.Previous.SHA256), shortHash(c.SHA256))
				}
				for _, r := range diff.Removed {
					fmt.Printf("  - removed: %s\n", r.URL)
				}
			}
		}
		if diff.Same {
			return 0
		}
		return 1

	default:
		fmt.Fprintf(os.Stderr, "[recon] unknown subcommand: %s (expected pull or diff)\n", sub)
		return 2
	}
}

func shortHash(h string) string {
	if len(h) > 10 {
		return h[:10]
	}
	return h
}
