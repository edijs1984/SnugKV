package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"runtime/debug"
	"snugkv/internal/config"
	"snugkv/internal/engine"
	"snugkv/internal/persistence"
	"snugkv/internal/server"
	"strings"
	"syscall"
	"time"
)

func main() {
	path := ""
	for i, arg := range os.Args[1:] {
		arg = strings.TrimPrefix(arg, "-")
		arg = "-" + strings.TrimPrefix(arg, "-")
		if strings.HasPrefix(arg, "-config=") {
			path = strings.TrimPrefix(arg, "-config=")
		}
		if (arg == "-config" || arg == "--config") && i+2 < len(os.Args) {
			path = os.Args[i+2]
		}
	}
	cfg, err := config.Load(path)
	if err != nil {
		log.Fatalf("configuration: %v", err)
	}
	if err = cfg.ApplyEnv(); err != nil {
		log.Fatal(err)
	}
	flag.StringVar(&path, "config", path, "strict JSON configuration file")
	flag.StringVar(&cfg.ListenAddr, "listen", cfg.ListenAddr, "TCP listen address")
	flag.StringVar(&cfg.MasterUser, "masteruser", cfg.MasterUser, "replication upstream ACL username (optional)")
	flag.StringVar(&cfg.MasterAuth, "masterauth", cfg.MasterAuth, "replication upstream password (optional)")
	flag.BoolVar(&cfg.MasterTLS, "mastertls", cfg.MasterTLS, "enable TLS for replication upstream")
	flag.StringVar(&cfg.MasterTLSCACert, "mastertls-ca-cert", cfg.MasterTLSCACert, "CA certificate for replication upstream TLS")
	flag.StringVar(&cfg.MasterTLSCert, "mastertls-cert", cfg.MasterTLSCert, "client certificate for replication upstream mTLS (optional)")
	flag.StringVar(&cfg.MasterTLSKey, "mastertls-key", cfg.MasterTLSKey, "client private key for replication upstream mTLS (optional)")
	flag.StringVar(&cfg.MasterTLSServerName, "mastertls-server-name", cfg.MasterTLSServerName, "TLS server name override for replication upstream (optional)")
	flag.IntVar(&cfg.Shards, "shards", cfg.Shards, "power-of-two shard count")
	flag.IntVar(&cfg.MaxConnections, "max-connections", cfg.MaxConnections, "maximum simultaneous clients")
	flag.Int64Var(&cfg.ReadTimeoutMS, "read-timeout-ms", cfg.ReadTimeoutMS, "request deadline in milliseconds")
	flag.Int64Var(&cfg.WriteTimeoutMS, "write-timeout-ms", cfg.WriteTimeoutMS, "response deadline in milliseconds")
	flag.IntVar(&cfg.MaxRequestBytes, "max-request-bytes", cfg.MaxRequestBytes, "maximum command bytes")
	flag.IntVar(&cfg.MaxBulkBytes, "max-bulk-bytes", cfg.MaxBulkBytes, "maximum bulk bytes")
	flag.IntVar(&cfg.MaxArguments, "max-arguments", cfg.MaxArguments, "maximum command arguments")
	flag.Int64Var(&cfg.CleanupIntervalMS, "cleanup-interval-ms", cfg.CleanupIntervalMS, "expiration cleanup interval")
	flag.Uint64Var(&cfg.MaxMemory, "max-memory", cfg.MaxMemory, "accounted memory budget in bytes, zero unlimited")
	flag.Int64Var(&cfg.GoMemoryLimit, "go-memory-limit", cfg.GoMemoryLimit, "Go runtime soft memory limit in bytes, zero preserves the existing runtime/GOMEMLIMIT setting")
	flag.BoolVar(&cfg.Encoding, "encoding", cfg.Encoding, "enable verified cheap codecs")
	flag.StringVar(&cfg.AOFPath, "aof", cfg.AOFPath, "append-only file path (optional)")
	flag.StringVar(&cfg.SnapshotPath, "snapshot", cfg.SnapshotPath, "snapshot file path (optional)")
	flag.StringVar(&cfg.Fsync, "fsync", cfg.Fsync, "always, everysec, or no")
	flag.BoolVar(&cfg.JSONShape, "json-shape", cfg.JSONShape, "enable background exact JSON template sharing")
	flag.BoolVar(&cfg.Compression, "compression", cfg.Compression, "enable background LZ4/Zstandard")
	flag.StringVar(&cfg.OptimizerMode, "optimizer-mode", cfg.OptimizerMode, "optimizer mode: dedicated or sidecar")
	flag.StringVar(&cfg.MetricsAddr, "metrics-listen", cfg.MetricsAddr, "separate loopback metrics address (optional)")
	flag.StringVar(&cfg.EvictionPolicy, "eviction-policy", cfg.EvictionPolicy, "noeviction, allkeys-lru, or volatile-lru")
	flag.StringVar(&cfg.AdminAddr, "admin-listen", cfg.AdminAddr, "separate loopback RESP admin address")
	pprofAddr := flag.String("pprof-listen", "", "optional loopback pprof HTTP address")
	flag.Parse()
	if flag.NArg() != 0 {
		log.Fatal("unexpected positional arguments")
	}
	if err = cfg.Validate(); err != nil {
		log.Fatal(err)
	}
	if *pprofAddr != "" {
		host, _, splitErr := net.SplitHostPort(*pprofAddr)
		if splitErr != nil {
			log.Fatalf("pprof-listen: %v", splitErr)
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() && !ip.IsUnspecified() {
			log.Fatal("pprof-listen must use a loopback or unspecified IP")
		}
		go func() {
			log.Printf("event=pprof_started listen=%s", *pprofAddr)
			if serveErr := http.ListenAndServe(*pprofAddr, nil); serveErr != nil {
				log.Printf("event=pprof_stopped error=%q", serveErr)
			}
		}()
	}
	if cfg.GoMemoryLimit > 0 {
		debug.SetMemoryLimit(cfg.GoMemoryLimit)
	}
	store, err := engine.NewWithOptions(engine.Options{Shards: cfg.Shards, MaxMemory: cfg.MaxMemory, Encoding: cfg.Encoding, ShapeEncoding: cfg.JSONShape, Compression: cfg.Compression})
	if err != nil {
		log.Fatal(err)
	}
	var recoveredReplication *persistence.ReplicationCheckpoint
	var journal *persistence.Log
	if cfg.AOFPath != "" {
		journal, err = persistence.Open(cfg.AOFPath, cfg.Fsync)
		if err != nil {
			log.Fatal(err)
		}
	}
	if cfg.SnapshotPath != "" {
		if err = persistence.ReplaySnapshot(cfg.SnapshotPath, func(records []persistence.Record) error {
			recoveredReplication = persistence.RecoverReplicationCheckpoint(recoveredReplication, records)
			return store.Restore(records, false)
		}); err != nil {
			log.Fatalf("snapshot recovery: %v", err)
		}
	}
	if cfg.AOFPath != "" {
		if err = persistence.Replay(cfg.AOFPath, func(records []persistence.Record) error {
			recoveredReplication = persistence.RecoverReplicationCheckpoint(recoveredReplication, records)
			return store.Restore(records, false)
		}); err != nil {
			log.Fatalf("AOF recovery: %v", err)
		}
	}
	var j server.Journal
	if journal != nil {
		j = journal
	}
	listener, err := server.ListenWithJournal(cfg, store, j)
	if err != nil {
		log.Fatal(err)
	}
	if err = listener.ConfigureFunctionPersistence(cfg.AOFPath, cfg.SnapshotPath); err != nil {
		listener.Close()
		log.Fatal(err)
	}
	if err = listener.ConfigureSearchPersistence(cfg.AOFPath, cfg.SnapshotPath); err != nil {
		listener.Close()
		log.Fatal(err)
	}
	if err = listener.ConfigureReplicationPersistenceRecovered(cfg.AOFPath, cfg.SnapshotPath, recoveredReplication); err != nil {
		listener.Close()
		log.Fatal(err)
	}
	if cfg.AdminAddr != "" {
		if err = listener.OpenAdmin(cfg.AdminAddr); err != nil {
			listener.Close()
			log.Fatal(err)
		}
	}
	var metrics *http.Server
	if cfg.MetricsAddr != "" {
		metrics, err = listener.Metrics(cfg.MetricsAddr)
		if err != nil {
			listener.Close()
			log.Fatal(err)
		}
	}
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	ticker := time.NewTicker(time.Duration(cfg.CleanupIntervalMS) * time.Millisecond)
	defer ticker.Stop()
	log.Printf("event=started listen=%s shards=%d", cfg.ListenAddr, cfg.Shards)
	for {
		select {
		case <-signals:
			listener.Close()
			if metrics != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				metrics.Shutdown(ctx)
				cancel()
			}

			checkpointReplication := listener.HasReplicationContinuationState()
			if checkpointReplication && journal != nil {
				if err = journal.Rewrite(store.Export(nil)); err != nil {
					log.Printf("event=replication_aof_checkpoint_failed error=%q", err)
					checkpointReplication = false
				}
			}
			if journal != nil {
				if err = journal.Close(); err != nil {
					log.Printf("event=persistence_close_failed error=%q", err)
					checkpointReplication = false
				}
			}
			if cfg.SnapshotPath != "" {
				if err = persistence.Snapshot(cfg.SnapshotPath, store.Export(nil)); err != nil {
					log.Printf("event=snapshot_failed error=%q", err)
					checkpointReplication = false
				}
			}
			if checkpointReplication {
				if err = listener.CheckpointReplicationPersistence(); err != nil {
					log.Printf("event=replication_checkpoint_failed error=%q", err)
				}
			}
			log.Print("event=stopped")
			return
		case <-ticker.C:
			listener.Maintain()
		}
	}
}
