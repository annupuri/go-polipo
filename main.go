package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
)

func usage(w io.Writer) {
	fmt.Fprintf(w, "Usage of go-polipo:\n")
	fmt.Fprintf(w, "  polipo [ -h ] [ -v ] [ -x ] [ -c filename ] [ -- ] [ var=val... ]\n")
	fmt.Fprintf(w, "Options:\n")
	fmt.Fprintf(w, "  -h            Display this message.\n")
	fmt.Fprintf(w, "  -v            Display the list of configuration variables and version.\n")
	fmt.Fprintf(w, "  -x            Perform expiry on the disk cache.\n")
	fmt.Fprintf(w, "  -c filename   Specify the configuration file to use.\n")
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run parses arguments, builds the configuration and either performs a
// one-shot action (-h, -v, -x) or starts the proxy server. It returns the
// process exit code. Splitting this out of main keeps the startup logic
// testable without spawning a process.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("go-polipo", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { usage(stderr) }

	var (
		help                 bool
		showVersionAndConfig bool
		expireCache          bool
		configFile           string
	)
	fs.BoolVar(&help, "h", false, "display help")
	fs.BoolVar(&showVersionAndConfig, "v", false, "show version and config variables")
	fs.BoolVar(&expireCache, "x", false, "expire disk cache")
	fs.StringVar(&configFile, "c", "", "config file path")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	if help {
		usage(stdout)
		return 0
	}

	config := NewDefaultConfig()

	// Locate default config files if not specified.
	if configFile == "" {
		if home, err := os.UserHomeDir(); err == nil {
			if p := filepath.Join(home, ".polipo"); fileExists(p) {
				configFile = p
			}
		}
		if configFile == "" && fileExists("/etc/polipo/config") {
			configFile = "/etc/polipo/config"
		}
	}

	if configFile != "" {
		logf(config.LogLevel, LogInfo, "Loading config file: %s", configFile)
		if err := config.LoadConfig(configFile); err != nil {
			fmt.Fprintf(stderr, "Error loading config: %v\n", err)
			return 1
		}
	}

	// Parse remaining command line arguments like var=val.
	for _, arg := range fs.Args() {
		if strings.Contains(arg, "=") {
			if err := config.ParseLine(arg); err != nil {
				fmt.Fprintf(stderr, "Error parsing command line variable '%s': %v\n", arg, err)
				return 1
			}
		}
	}

	if showVersionAndConfig {
		printVersionAndConfig(stdout, config)
		return 0
	}

	if expireCache {
		cm := NewCacheManager(config)
		stats, err := cm.Expire()
		if err != nil {
			fmt.Fprintf(stderr, "Disk cache expiry failed: %v\n", err)
			return 1
		}
		fmt.Fprintf(stdout, "Disk cache expiry complete: scanned %d, removed %d (%d for exceeding diskCacheTruncateSize while stale), freed %d bytes\n",
			stats.Scanned, stats.Removed, stats.RemovedForSize, stats.Freed)
		return 0
	}

	// Redirect the logger to a file when asked. This matters for background
	// use on Windows, where a -H windowsgui build has no console for stderr.
	var reopenLog func() error
	if config.LogFile != "" {
		w, err := openLogFileWriter(config.LogFile, config.LogFilePermissions)
		if err != nil {
			fmt.Fprintf(stderr, "Cannot open logFile %q: %v\n", config.LogFile, err)
			return 1
		}
		defer func() {
			log.SetOutput(os.Stderr)
			w.Close()
		}()
		log.SetOutput(w)
		reopenLog = w.Reopen
	}

	return serve(config, reopenLog)
}

// serve builds the proxy handler and blocks serving requests until the
// process receives SIGINT or SIGTERM. All of its messages go through the log
// package (respecting logFile), so headless deployments (e.g. a Windows Task
// Scheduler entry with no console) still leave a record of whether the
// server actually started. reopenLog, nil when logFile is unset, reopens the
// log file on SIGUSR1/SIGUSR2.
func serve(config *Config, reopenLog func() error) int {
	forbidden := NewForbiddenList()
	if err := forbidden.Load(config.ForbiddenFile, config.LogLevel); err != nil {
		logf(config.LogLevel, LogWarn, "Warning: failed to load forbidden file %s: %v", config.ForbiddenFile, err)
	}
	uncachable := NewForbiddenList()
	if err := uncachable.Load(config.UncachableFile, config.LogLevel); err != nil {
		logf(config.LogLevel, LogWarn, "Warning: failed to load uncachable file %s: %v", config.UncachableFile, err)
	}
	forbiddenTunnels := NewForbiddenList()
	if err := forbiddenTunnels.Load(config.ForbiddenTunnelsFile, config.LogLevel); err != nil {
		logf(config.LogLevel, LogWarn, "Warning: failed to load forbiddenTunnels file %s: %v", config.ForbiddenTunnelsFile, err)
	}

	cacheMgr := NewCacheManager(config)
	handler := NewProxyHandler(config, cacheMgr, forbidden, uncachable, forbiddenTunnels)
	setupReloadSignals(config.LogLevel, reopenLog, handler.Reload, cacheMgr.DiscardMemory)

	addr := net.JoinHostPort(config.ProxyAddress, fmt.Sprintf("%d", config.ProxyPort))
	server := newServer(config, addr, handler)

	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
		<-sigChan
		logf(config.LogLevel, LogInfo, "Shutting down go-polipo...")
		server.Close()
	}()

	logf(config.LogLevel, LogInfo, "go-polipo version %s starting on http://%s", Version, addr)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		logf(config.LogLevel, LogError, "Server error: %v", err)
		return 1
	}
	return 0
}

// newServer builds the client-facing server. clientTimeout bounds a client's
// request headers and its idle time between requests, as Polipo's did. It
// does not bound a request or response body, which may legitimately take
// longer, nor a CONNECT tunnel: the server clears the header deadline once
// the headers are read, before the tunnel takes the connection over.
func newServer(config *Config, addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: config.ClientTimeout,
		IdleTimeout:       config.ClientTimeout,
	}
}

func printVersionAndConfig(w io.Writer, config *Config) {
	fmt.Fprintf(w, "go-polipo version %s\n", Version)
	fmt.Fprintln(w, "Configuration variables:")
	for _, e := range config.Entries() {
		fmt.Fprintf(w, "  %s = %s\n", e.Name, e.Value)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
