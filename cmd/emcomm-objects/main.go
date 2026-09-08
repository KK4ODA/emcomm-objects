// Command emcomm-objects is a companion to Graywolf APRS for managing and
// beaconing APRS objects (events, served agencies, deployed units).
//
//	emcomm-objects                   run: transport + scheduler + web UI
//	emcomm-objects --beacon NAME     send one live beacon, exit
//	emcomm-objects --list            list objects, exit
//	emcomm-objects --init-config     write an annotated config.yaml, exit
//	emcomm-objects --version
//
// With no config file the app still starts and the browser UI asks for the
// callsign and Graywolf login (Settings), so a fresh install is usable
// without touching a text editor.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kk4oda/emcomm-objects/internal/config"
	"github.com/kk4oda/emcomm-objects/internal/graywolf"
	"github.com/kk4oda/emcomm-objects/internal/kiss"
	"github.com/kk4oda/emcomm-objects/internal/link"
	"github.com/kk4oda/emcomm-objects/internal/logbuf"
	"github.com/kk4oda/emcomm-objects/internal/paths"
	"github.com/kk4oda/emcomm-objects/internal/planner"
	"github.com/kk4oda/emcomm-objects/internal/scheduler"
	"github.com/kk4oda/emcomm-objects/internal/store"
	"github.com/kk4oda/emcomm-objects/internal/transmit"
	"github.com/kk4oda/emcomm-objects/internal/update"
	"github.com/kk4oda/emcomm-objects/internal/version"
	"github.com/kk4oda/emcomm-objects/internal/web"
)

func main() {
	os.Exit(run())
}

func run() int {
	var (
		dataArg    = flag.String("data", "", "data directory (config, objects, log); default depends on install type")
		cfgArg     = flag.String("config", "", "path to config.yaml (default: <data>/config.yaml)")
		objectsArg = flag.String("objects", "", "override objects file path")
		beaconName = flag.String("beacon", "", "send one live beacon for this object name, then exit")
		listFlag   = flag.Bool("list", false, "list objects and exit")
		initFlag   = flag.Bool("init-config", false, "write an annotated config.yaml and exit")
		noBrowser  = flag.Bool("no-browser", false, "do not open the web UI in a browser on start")
		verbose    = flag.Bool("v", false, "verbose (debug) logging")
		showVer    = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVer {
		fmt.Println("emcomm-objects", version.String())
		return 0
	}

	dataDir := paths.DataDir()
	if *dataArg != "" {
		dataDir = *dataArg
	}
	cfgPath := paths.Resolve(dataDir, paths.ConfigFile)
	if *cfgArg != "" {
		cfgPath = *cfgArg
	}
	mode := paths.InstallMode()

	if *initFlag {
		if err := config.WriteExample(cfgPath); err != nil {
			fmt.Fprintf(os.Stderr, "init-config: %v\n", err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "wrote %s\n", cfgPath)
		return 0
	}

	// Logging: stderr + a file in the data dir + an in-memory ring for the
	// UI. Windows release builds run without a console, so there the file
	// and the UI are the only logs.
	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	broker := web.NewBroker(200)
	sinks := []slog.Handler{slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})}
	if f := openLogFile(paths.Resolve(dataDir, paths.LogFile)); f != nil {
		defer f.Close()
		sinks = append(sinks, slog.NewTextHandler(f, &slog.HandlerOptions{Level: level}))
	}
	logs := logbuf.New(level, 400, broker.LogHook(), sinks...)
	log := slog.New(logs)
	slog.SetDefault(log)

	cfg, err := config.Load(cfgPath)
	setupNeeded := false
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "config: %v\n", err)
			return 1
		}
		setupNeeded = true
		log.Info("no config yet; open the web UI to set up", "path", cfgPath)
	}
	if *objectsArg != "" {
		cfg.Storage.ObjectsFile = *objectsArg
	}
	objectsPath := paths.Resolve(dataDir, cfg.Storage.ObjectsFile)

	st := store.New(objectsPath)
	if err := st.Load(); err != nil {
		fmt.Fprintf(os.Stderr, "load objects: %v\n", err)
		return 1
	}

	if *listFlag {
		listObjects(st)
		return 0
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Both transports exist; the router activates the configured one and
	// the state hook also wakes the scheduler whenever a link comes up.
	var sch *scheduler.Scheduler
	stateHook := broker.StateHook()
	onState := func(s link.State) {
		stateHook(s)
		if s.Status == link.Connected && sch != nil {
			sch.Wake()
		}
	}
	gwClient := graywolf.NewClient(cfg.Graywolf.URL, cfg.Graywolf.Username, cfg.Graywolf.Password)
	gwLink := graywolf.NewTransport(gwClient, graywolf.Options{Channel: cfg.Graywolf.Channel, SendPath: cfg.Graywolf.SendPath},
		log.With("comp", "graywolf"), onState)
	kissLink := kiss.NewClient(cfg.KISS.Address, byte(cfg.KISS.Port), log.With("comp", "kiss"), onState)
	router := transmit.NewRouter(gwLink, kissLink)
	go gwLink.Run(ctx)
	go kissLink.Run(ctx)
	if !setupNeeded {
		_ = router.Use(cfg.Transport)
	}

	sender := transmit.NewSender(cfg, router, log.With("comp", "tx"))
	sender.OnPacket = broker.PacketHook()

	if *beaconName != "" {
		return beaconOnce(ctx, st, sender, router, *beaconName)
	}

	sch = scheduler.New(st, sender.Transmit, sender.TransmitKilled, scheduler.Options{
		Retire: sender.Retire,
		Ready:  sender.Ready,
		Logger: log.With("comp", "sched"),
	})
	st.SetOnSave(func() { broker.Notify(web.EventObjects) })

	var bridge *planner.Bridge

	// Live config + hot apply for Settings changes.
	var cfgMu sync.RWMutex
	current := cfg
	getConfig := func() config.Config {
		cfgMu.RLock()
		defer cfgMu.RUnlock()
		return current
	}
	applyConfig := func(next config.Config) error {
		if err := config.Save(cfgPath, next); err != nil {
			return err
		}
		cfgMu.Lock()
		current = next
		cfgMu.Unlock()
		sender.SetConfig(next)
		gwLink.Reconfigure(next.Graywolf.URL, next.Graywolf.Username, next.Graywolf.Password,
			graywolf.Options{Channel: next.Graywolf.Channel, SendPath: next.Graywolf.SendPath})
		kissLink.Reconfigure(next.KISS.Address, byte(next.KISS.Port))
		if err := router.Use(next.Transport); err != nil {
			return err
		}
		sch.Wake()
		if bridge != nil {
			bridge.Wake()
		}
		log.Info("settings saved", "path", cfgPath, "transport", next.Transport, "callsign", next.Station.Callsign)
		return nil
	}

	updater := update.New(version.Version, mode, dataDir,
		func() update.Settings {
			u := getConfig().Updates
			return update.Settings{Check: u.Check, IntervalHours: u.IntervalHours, SkipVersion: u.SkipVersion}
		},
		func(v string) error {
			next := getConfig()
			next.Updates.SkipVersion = v
			return applyConfig(next)
		},
		log.With("comp", "update"))
	go updater.Run(ctx)

	// EmComm Planner link: forwards Graywolf's heard stations and sends the
	// planner's queued APRS messages. Idles while disabled in Settings.
	bridge = planner.New(gwClient, getConfig, log.With("comp", "planner"), func(planner.State) { broker.Notify(web.EventConfig) })
	go bridge.Run(ctx)

	log.Info("emcomm-objects starting",
		"version", version.String(), "mode", string(mode), "data", dataDir,
		"transport", cfg.Transport, "callsign", cfg.Station.Callsign, "objects", objectsPath,
		"web", cfg.Web.Listen)

	if cfg.Web.Enabled {
		srv := web.New(web.Deps{
			Store: st, Scheduler: sch, Link: router, Broker: broker, Logs: logs, Updater: updater, Planner: bridge,
			Log:         log.With("comp", "web"),
			Config:      getConfig,
			ApplyConfig: applyConfig,
			Retire:      sender.Retire,
			Quit:        stop,
			DataDir:     dataDir, ConfigPath: cfgPath, Mode: string(mode),
		})
		ready := func(addr string) {
			if cfg.Web.OpenBrowser && !*noBrowser {
				openBrowser("http://" + displayAddr(addr) + "/")
			}
		}
		go func() {
			if err := srv.ListenAndServe(ctx, cfg.Web.Listen, ready); err != nil {
				if isAddrInUse(err) && alreadyRunning(cfg.Web.Listen) {
					log.Error("another emcomm-objects is already running; opening its UI", "listen", cfg.Web.Listen)
					if !*noBrowser {
						openBrowser("http://" + displayAddr(cfg.Web.Listen) + "/")
					}
				} else {
					log.Error("web server stopped", "err", err)
				}
				stop()
			}
		}()
	}

	sch.Run(ctx)
	log.Info("shut down cleanly")
	return 0
}

func beaconOnce(ctx context.Context, st *store.Store, sender *transmit.Sender, router *transmit.Router, name string) int {
	o, ok := st.Get(name)
	if !ok {
		fmt.Fprintf(os.Stderr, "object %q not found\n", name)
		return 2
	}
	deadline := time.Now().Add(8 * time.Second)
	for router.State().Status != link.Connected && time.Now().Before(deadline) && ctx.Err() == nil {
		time.Sleep(100 * time.Millisecond)
	}
	if router.State().Status != link.Connected {
		fmt.Fprintf(os.Stderr, "transport not connected: %s\n", router.State().Detail)
		return 1
	}
	now := time.Now().UTC()
	if err := sender.Transmit(o, now); err != nil {
		fmt.Fprintf(os.Stderr, "transmit: %v\n", err)
		return 1
	}
	st.MarkBeaconed(o.ObjectName, now)
	if err := st.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: persist LastBeacon: %v\n", err)
	}
	fmt.Fprintf(os.Stderr, "beacon sent for %s\n", o.ObjectName)
	return 0
}

func listObjects(st *store.Store) {
	objs := st.List()
	if len(objs) == 0 {
		fmt.Println("(no objects)")
		return
	}
	for _, o := range objs {
		flag := "  "
		switch {
		case o.IsKilled():
			flag = "x "
		case o.Enabled:
			flag = "ok"
		}
		last := "never"
		if !o.LastBeacon.IsZero() {
			last = o.LastBeacon.UTC().Format(time.RFC3339)
		}
		fmt.Printf("%-2s %-9s  %s%s %9.5f %10.5f  every %3dm  last=%s  %s\n",
			flag, o.ObjectName, o.SymbolTable, o.SymbolID, o.Latitude, o.Longitude, o.IntervalMinutes, last, o.Comment)
	}
}

// openLogFile opens the log file, rotating it once it passes 2 MB; nil on
// failure (logging then goes to stderr / the UI only).
func openLogFile(path string) *os.File {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil
	}
	if fi, err := os.Stat(path); err == nil && fi.Size() > 2<<20 {
		_ = os.Rename(path, strings.TrimSuffix(path, ".log")+".old.log")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil
	}
	return f
}

func displayAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

func isAddrInUse(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return errors.Is(err, syscall.EADDRINUSE) || strings.Contains(msg, "address already in use") ||
		strings.Contains(msg, "only one usage of each socket address")
}

// alreadyRunning checks whether whatever holds the port is emcomm-objects.
func alreadyRunning(listen string) bool {
	c := &http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get("http://" + displayAddr(listen) + "/api/version")
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return resp.StatusCode == 200 && strings.Contains(string(body), `"version"`)
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	case "darwin":
		cmd = exec.Command("open", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	if err := cmd.Start(); err != nil {
		slog.Warn("could not open browser", "url", url, "err", err)
		return
	}
	go func() { _ = cmd.Wait() }()
}
