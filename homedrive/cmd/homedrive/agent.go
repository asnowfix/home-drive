// agent.go wires the full homedrive sync engine per PLAN.md §3.2: watcher,
// push syncer, pull (Changes API), bisync safety net, MQTT publisher, and
// HTTP control endpoint, all sharing one BoltDB journal and one bisync
// coordination mutex.
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/user"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	httpctl "github.com/asnowfix/home-drive/homedrive/internal/http"

	"github.com/asnowfix/home-drive/homedrive/internal/config"
	"github.com/asnowfix/home-drive/homedrive/internal/mqtt"
	"github.com/asnowfix/home-drive/homedrive/internal/rcloneclient"
	"github.com/asnowfix/home-drive/homedrive/internal/store"
	"github.com/asnowfix/home-drive/homedrive/internal/syncer"
	"github.com/asnowfix/home-drive/homedrive/internal/watcher"
)

// Agent owns every long-running component of the homedrive sync engine
// and implements the httpctl.Deps interfaces so the HTTP control endpoint
// can query and control it.
type Agent struct {
	cfg        *config.Config
	configPath string
	version    string
	log        *slog.Logger
	logLevel   *slog.LevelVar // nil in tests that don't need live level changes

	journal   *store.Journal
	auditFile *os.File // nil if audit logging is disabled

	rfs remoteFS

	mqttReal *mqtt.Client // non-nil only when MQTT is enabled and connected

	watch      *watcher.Watcher
	pushSyncer *syncer.Syncer
	puller     *syncer.Puller
	bisync     *syncer.Bisync

	// bisyncMu is shared between the push syncer (RLock), the pull loop
	// (RLock, taken by this package around each PollOnce call since
	// syncer.Puller does not itself coordinate with bisync), and bisync
	// (Lock). See PLAN.md §7.2.
	bisyncMu *sync.RWMutex

	httpSrv *httpctl.Server

	// pushEvents/pushRenames are the channels between the watcher pump
	// and the push syncer worker pool.
	pushEvents  chan syncer.Event
	pushRenames chan syncer.DirRename

	paused         atomic.Bool
	reloadedDryRun atomic.Bool
	startTime      time.Time

	lastPushUnixNano atomic.Int64
	lastPullUnixNano atomic.Int64
}

// AgentOpts groups the inputs needed to build an Agent, kept separate from
// Agent itself so tests can construct partially-wired agents directly.
type AgentOpts struct {
	ConfigPath string
	DryRun     bool
	Version    string
	Log        *slog.Logger
	LogLevel   *slog.LevelVar
}

// newAgent loads configuration and constructs every component of the sync
// engine, wiring them together per PLAN.md §3.2. It does not start any
// goroutines; call Run to do that.
func newAgent(ctx context.Context, opts AgentOpts) (*Agent, error) {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}

	cfg, err := config.Load(opts.ConfigPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	if opts.DryRun {
		cfg.DryRun = true
	}

	if err := os.MkdirAll(filepath.Dir(cfg.State.Path), 0o755); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	if err := os.MkdirAll(cfg.LocalRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create local root: %w", err)
	}

	journal, err := store.OpenJournal(cfg.State.Path, log)
	if err != nil {
		return nil, fmt.Errorf("open journal: %w", err)
	}

	auditAdapter, rawAudit, auditFile, err := buildAuditLog(cfg, log)
	if err != nil {
		_ = journal.Close()
		return nil, err
	}

	rfs, err := rcloneclient.NewRcloneFS(ctx, rcloneclient.RcloneFSConfig{
		Remote:     cfg.Remote,
		ConfigPath: cfg.RcloneConfig,
		Exclude:    cfg.Watcher.Exclude,
		Log:        log,
	})
	if err != nil {
		_ = journal.Close()
		closeAuditFile(auditFile, log)
		return nil, fmt.Errorf("init rclone remote: %w", err)
	}

	pub, mqttReal, err := buildPublisher(cfg, log)
	if err != nil {
		_ = journal.Close()
		closeAuditFile(auditFile, log)
		return nil, err
	}

	a := &Agent{
		cfg:         cfg,
		configPath:  opts.ConfigPath,
		version:     opts.Version,
		log:         log,
		logLevel:    opts.LogLevel,
		journal:     journal,
		auditFile:   auditFile,
		rfs:         rfs,
		mqttReal:    mqttReal,
		bisyncMu:    &sync.RWMutex{},
		pushEvents:  make(chan syncer.Event, cfg.Push.Workers*4+4),
		pushRenames: make(chan syncer.DirRename, cfg.Push.Workers*4+4),
	}
	a.reloadedDryRun.Store(cfg.DryRun)

	if err := a.buildWatcher(); err != nil {
		a.closeResources()
		return nil, err
	}
	a.buildPushSyncer(pub, auditAdapter)
	a.buildPuller(pub, auditAdapter)
	a.buildBisync(pub, rawAudit)
	a.buildHTTPServer()

	return a, nil
}

// buildAuditLog opens the JSONL audit log file (if configured) and returns
// the syncer.AuditLogger adapter, the raw io.Writer for bisync, and the
// underlying *os.File so it can be closed on shutdown.
func buildAuditLog(cfg *config.Config, log *slog.Logger) (syncer.AuditLogger, io.Writer, *os.File, error) {
	if cfg.State.AuditLog == "" {
		return nil, nil, nil, nil
	}
	if err := os.MkdirAll(filepath.Dir(cfg.State.AuditLog), 0o755); err != nil {
		return nil, nil, nil, fmt.Errorf("create audit log dir: %w", err)
	}
	f, err := os.OpenFile(cfg.State.AuditLog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open audit log: %w", err)
	}
	adapter := &auditLoggerAdapter{a: store.NewAuditor(f, log)}
	return adapter, f, f, nil
}

func closeAuditFile(f *os.File, log *slog.Logger) {
	if f == nil {
		return
	}
	if err := f.Close(); err != nil {
		log.Error("audit file close error", "error", err)
	}
}

// buildPublisher constructs a real MQTT publisher when cfg.MQTT.Enabled is
// true, or a no-op publisher otherwise. This is the fix for the gap
// described in issue #49: production previously always used noopPublisher.
func buildPublisher(cfg *config.Config, log *slog.Logger) (syncer.Publisher, *mqtt.Client, error) {
	if !cfg.MQTT.Enabled {
		return noopPublisher{}, nil, nil
	}

	host, username := hostAndUser()
	client, err := mqtt.New(mqtt.Config{
		Broker:            cfg.MQTT.Broker,
		ClientIDPrefix:    cfg.MQTT.ClientIDPrefix,
		BaseTopic:         cfg.MQTT.BaseTopic,
		HADiscoveryPrefix: cfg.MQTT.HADiscoveryPrefix,
		QoS:               cfg.MQTT.QoS,
	}, host, username, log)
	if err != nil {
		return nil, nil, fmt.Errorf("connect mqtt: %w", err)
	}
	return client, client, nil
}

// hostAndUser returns the local hostname and OS username used to build
// unique MQTT client IDs and topic paths.
func hostAndUser() (string, string) {
	host, err := os.Hostname()
	if err != nil || host == "" {
		host = "unknown-host"
	}
	name := "unknown-user"
	if u, err := user.Current(); err == nil && u.Username != "" {
		name = u.Username
	}
	return host, name
}

// buildWatcher constructs the fsnotify watcher feeding the push job queue.
func (a *Agent) buildWatcher() error {
	wcfg := watcher.Config{
		LocalRoot:           a.cfg.LocalRoot,
		Debounce:            a.cfg.Watcher.Debounce.Duration,
		DirRenamePairWindow: a.cfg.Watcher.DirRenamePairWindow.Duration,
		Exclude:             a.cfg.Watcher.Exclude,
	}
	w, err := watcher.New(wcfg, &watcherStoreAdapter{j: a.journal}, a.log)
	if err != nil {
		return fmt.Errorf("init watcher: %w", err)
	}
	a.watch = w
	return nil
}

// buildPushSyncer constructs the push worker pool per cfg.Push.
func (a *Agent) buildPushSyncer(pub syncer.Publisher, audit syncer.AuditLogger) {
	pushCfg := syncer.Config{
		Workers: a.cfg.Push.Workers,
		Retry: syncer.RetryConfig{
			MaxAttempts:    a.cfg.Push.Retry.MaxAttempts,
			InitialBackoff: a.cfg.Push.Retry.InitialBackoff.Duration,
			MaxBackoff:     a.cfg.Push.Retry.MaxBackoff.Duration,
		},
		DryRun:    a.cfg.DryRun,
		LocalRoot: a.cfg.LocalRoot,
	}
	a.pushSyncer = syncer.New(
		pushCfg,
		&rcloneSyncerAdapter{fs: a.rfs},
		&journalSyncerAdapter{j: a.journal, logger: a.log},
		audit,
		pub,
		a.log,
		syncer.WithBisyncMutex(a.bisyncMu),
	)
}

// buildPuller constructs the Drive Changes API poller per cfg.Pull.
func (a *Agent) buildPuller(pub syncer.Publisher, audit syncer.AuditLogger) {
	interval := a.cfg.Pull.ChangesAPIInterval.Duration
	if interval <= 0 {
		interval = 30 * time.Second
	}
	a.puller = syncer.NewPuller(
		syncer.PullerConfig{
			Interval:       interval,
			LocalRoot:      a.cfg.LocalRoot,
			ConflictPolicy: syncer.ConflictPolicy(a.cfg.Conflict.Policy),
			DryRun:         a.cfg.DryRun,
		},
		&rcloneSyncerAdapter{fs: a.rfs},
		&journalSyncerAdapter{j: a.journal, logger: a.log},
		audit,
		pub,
		a.log,
		time.Now,
	)
}

// buildBisync constructs the hourly bisync safety net per cfg.Pull.BisyncInterval,
// sharing bisyncMu with the push syncer and the pull loop.
func (a *Agent) buildBisync(pub syncer.Publisher, rawAudit io.Writer) {
	interval := a.cfg.Pull.BisyncInterval.Duration
	if interval <= 0 {
		interval = time.Hour
	}
	b, _ := syncer.NewBisync(syncer.BisyncOpts{
		Config: syncer.BisyncConfig{
			Interval:  interval,
			LocalRoot: a.cfg.LocalRoot,
			DryRun:    a.cfg.DryRun,
		},
		Remote:  &rcloneSyncerAdapter{fs: a.rfs},
		Journal: &bisyncJournalAdapter{j: a.journal},
		MQTT:    pub,
		Audit:   rawAudit,
		Logger:  a.log,
		Mu:      a.bisyncMu,
	})
	a.bisync = b
}

// buildHTTPServer constructs the HTTP control endpoint per cfg.HTTP.
func (a *Agent) buildHTTPServer() {
	a.httpSrv = httpctl.NewServer(
		httpctl.ServerConfig{
			ListenAddr:    a.cfg.HTTP.Listen,
			EnableMetrics: a.cfg.HTTP.Metrics,
		},
		httpctl.Deps{
			Pausable:       a,
			Resyncable:     a,
			Reloadable:     a,
			StatusProvider: a,
			HealthChecker:  a,
		},
		nil,
		a.log,
	)
}

// closeResources releases everything opened during construction. Used when
// newAgent fails partway through.
func (a *Agent) closeResources() {
	if a.journal != nil {
		_ = a.journal.Close()
	}
	closeAuditFile(a.auditFile, a.log)
}
