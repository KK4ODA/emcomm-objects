// Command emcomm-objects is a companion to Graywolf APRS for managing and
// beaconing APRS objects (events, served agencies, deployed units).
//
// Subcommands:
//
//	emcomm-objects                     # run daemon (scheduler + web UI per config)
//	emcomm-objects --beacon NAME       # send one beacon, exit
//	emcomm-objects --list              # list objects, exit
//	emcomm-objects --init-config       # write example config.yaml and exit
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kk4oda/emcomm-objects/internal/config"
	"github.com/kk4oda/emcomm-objects/internal/kiss"
	"github.com/kk4oda/emcomm-objects/internal/scheduler"
	"github.com/kk4oda/emcomm-objects/internal/store"
	"github.com/kk4oda/emcomm-objects/internal/transmit"
	"github.com/kk4oda/emcomm-objects/internal/web"
)

func main() {
	var (
		cfgPath    = flag.String("config", "config.yaml", "path to YAML config")
		objectsArg = flag.String("objects", "", "override objects file path (defaults to storage.objects_file)")
		beaconName = flag.String("beacon", "", "send one beacon for this object name, then exit")
		listFlag   = flag.Bool("list", false, "list objects from the JSON store and exit")
		initFlag   = flag.Bool("init-config", false, "write an example config.yaml at -config path and exit")
		verbose    = flag.Bool("v", false, "verbose (debug) logging")
	)
	flag.Parse()

	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)

	if *initFlag {
		if err := config.WriteExample(*cfgPath); err != nil {
			fmt.Fprintf(os.Stderr, "init-config: %v\n", err)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "wrote example config to %s — edit, then re-run\n", *cfgPath)
		return
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || isMissingFile(err) {
			fmt.Fprintf(os.Stderr, "%v\n\nNo config found. Run:\n  %s --init-config\nto write an example, then edit and re-run.\n", err, os.Args[0])
		} else {
			fmt.Fprintf(os.Stderr, "config: %v\n", err)
		}
		os.Exit(1)
	}
	if *objectsArg != "" {
		cfg.Storage.ObjectsFile = *objectsArg
	}

	st := store.New(cfg.Storage.ObjectsFile)
	if err := st.Load(); err != nil {
		fmt.Fprintf(os.Stderr, "load objects: %v\n", err)
		os.Exit(1)
	}

	if *listFlag {
		objs := st.List()
		if len(objs) == 0 {
			fmt.Println("(no objects)")
			return
		}
		for _, o := range objs {
			enabled := "  "
			if o.Enabled {
				enabled = "ok"
			}
			last := "never"
			if !o.LastBeacon.IsZero() {
				last = o.LastBeacon.UTC().Format(time.RFC3339)
			}
			fmt.Printf("%-2s %-9s  %s%s %9.5f %10.5f  every %3dm  last=%s  %s\n",
				enabled, o.ObjectName, o.SymbolTable, o.SymbolID,
				o.Latitude, o.Longitude, o.IntervalMinutes, last, o.Comment)
		}
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	broker := web.NewBroker(50)
	kissClient := kiss.NewClient(cfg.KISS.Address, byte(cfg.KISS.Port), log.With("comp", "kiss"), broker.KISSStateHook())
	go kissClient.Run(ctx)

	sender := transmit.NewSender(cfg, kissClient, log.With("comp", "tx"))
	sender.OnPacket = broker.PacketHook()

	if *beaconName != "" {
		o, ok := st.Get(*beaconName)
		if !ok {
			fmt.Fprintf(os.Stderr, "object %q not found\n", *beaconName)
			os.Exit(2)
		}
		// Wait briefly for KISS to connect.
		if !waitFor(ctx, 3*time.Second, func() bool { return kissClient.State() == kiss.StateConnected }) {
			fmt.Fprintln(os.Stderr, "warning: KISS not connected after 3s, attempting send anyway")
		}
		if err := sender.Transmit(o, time.Now().UTC()); err != nil {
			fmt.Fprintf(os.Stderr, "transmit: %v\n", err)
			os.Exit(1)
		}
		st.MarkBeaconed(o.ObjectName, time.Now().UTC())
		if err := st.Save(); err != nil {
			fmt.Fprintf(os.Stderr, "warning: persist LastBeacon: %v\n", err)
		}
		fmt.Fprintf(os.Stderr, "beacon sent for %s\n", o.ObjectName)
		return
	}

	sch := scheduler.New(st, sender.Transmit, sender.TransmitKilled, scheduler.Options{
		TickInterval: 10 * time.Second,
		Logger:       log.With("comp", "sched"),
	})

	log.Info("daemon starting",
		"callsign", cfg.Station.Callsign,
		"kiss", cfg.KISS.Address,
		"objects", cfg.Storage.ObjectsFile,
		"web_enabled", cfg.Web.Enabled,
		"web_listen", cfg.Web.Listen,
	)

	if cfg.Web.Enabled {
		srv := web.New(cfg, st, sch, kissClient, broker, log.With("comp", "web"))
		go func() {
			if err := srv.ListenAndServe(ctx); err != nil {
				log.Error("web server stopped", "err", err)
			}
		}()
	}

	sch.Run(ctx)
	log.Info("shut down cleanly")
}

func isMissingFile(err error) bool {
	if err == nil {
		return false
	}
	// Wrapped os.ErrNotExist; config.Load wraps in fmt.Errorf with %w.
	return errors.Is(err, os.ErrNotExist)
}

func waitFor(ctx context.Context, timeout time.Duration, cond func() bool) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(100 * time.Millisecond):
		}
	}
	return cond()
}
