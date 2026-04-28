// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 BoanLab @ Dankook University

// outrelay-relay is the stateless relay data plane. It dials the
// controller's gRPC registry on startup, self-registers (UpsertRelay),
// and routes service registrations / resolves through it. The relay
// keeps a local URI -> connected-agent map so it can open
// INCOMING_STREAM toward the right QUIC connection.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	pb "github.com/boanlab/OutRelay/lib/control/v1"
	"github.com/boanlab/OutRelay/lib/observe"

	"github.com/boanlab/outrelay-relay/pkg/audit"
	"github.com/boanlab/outrelay-relay/pkg/edge"
	"github.com/boanlab/outrelay-relay/pkg/intra"
	"github.com/boanlab/outrelay-relay/pkg/policy"
	"github.com/boanlab/outrelay-relay/pkg/registry"
)

// Version is stamped at link time via -ldflags '-X main.Version=...'.
var Version = "dev"

func main() {
	var (
		listen          = flag.String("listen", "127.0.0.1:7443", "QUIC listen address")
		certPath        = flag.String("cert", "", "PEM-encoded server cert")
		keyPath         = flag.String("key", "", "PEM-encoded server key")
		caPath          = flag.String("ca", "", "PEM-encoded CA bundle for client cert verification")
		controllerAddr  = flag.String("controller", "127.0.0.1:7444", "controller gRPC address")
		relayID         = flag.String("relay-id", "", "this relay's id (advertised via UpsertRelay; defaults to listen addr)")
		region          = flag.String("region", "local", "this relay's region label")
		advertised      = flag.String("advertise", "", "endpoint advertised to agents (defaults to --listen)")
		policyTenant    = flag.String("tenant", "", "tenant whose policies to subscribe to (empty = no policy enforcement)")
		debugListen     = flag.String("debug-listen", "127.0.0.1:9100", "localhost-only debug HTTP (/debug/metrics, /debug/pprof). Empty disables.")
		metricsDump     = flag.String("metrics-dump", "", "JSONL file path for periodic metrics dump (empty disables)")
		metricsInterval = flag.Duration("metrics-interval", 10*time.Second, "metrics dump interval")
		logFormat       = flag.String("log-format", "text", "log format: text or json")
		showVersion     = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()
	if *showVersion {
		fmt.Println(Version)
		return
	}

	logger := newLogger(*logFormat)
	if *certPath == "" || *keyPath == "" || *caPath == "" {
		logger.Error("missing required flags", "cert", *certPath, "key", *keyPath, "ca", *caPath)
		os.Exit(2)
	}

	tlsConf, err := loadServerTLS(*certPath, *keyPath, *caPath)
	if err != nil {
		logger.Error("load tls", "err", err)
		os.Exit(1)
	}

	ctx, cancel := signalContext()
	defer cancel()

	cc, err := grpc.NewClient(*controllerAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		logger.Error("dial controller", "addr", *controllerAddr, "err", err)
		os.Exit(1)
	}
	defer cc.Close()
	ctrl := pb.NewRegistryClient(cc)

	id := *relayID
	if id == "" {
		id = *listen
	}
	advert := *advertised
	if advert == "" {
		advert = *listen
	}

	upsertCtx, upsertCancel := context.WithTimeout(ctx, 5*time.Second)
	if _, err := ctrl.UpsertRelay(upsertCtx, &pb.UpsertRelayRequest{
		Id: id, Region: *region, Endpoint: advert,
	}); err != nil {
		upsertCancel()
		logger.Error("upsert relay", "err", err)
		os.Exit(1)
	}
	upsertCancel()
	logger.Info("relay self-registered", "id", id, "controller", *controllerAddr, "version", Version)

	reg := registry.New(ctrl, id)

	var (
		policyEngine *policy.Engine
		policyCache  *policy.Cache
		auditEm      *audit.Emitter
	)
	if *policyTenant != "" {
		policyEngine = policy.NewEngine()
		policyCache = policy.NewCache()
		auditEm = audit.NewEmitter(pb.NewAuditClient(cc), logger)
		watcher := policy.NewWatcher(pb.NewPolicyClient(cc), *policyTenant, policyEngine, policyCache, logger)
		go func() { _ = watcher.Run(ctx) }()
		go func() { _ = auditEm.Run(ctx) }()
		logger.Info("policy enforcement enabled", "tenant", *policyTenant)
	}

	// Inter-relay forwarding pool. The relay reuses its own server
	// cert as a client cert when dialing peer relays — both sides see
	// the URI SAN and verify against the shared CA.
	pool := intra.NewPool(intraTLS(tlsConf))
	defer func() { _ = pool.Close() }()

	// Observability — shared registry, optional debug HTTP and JSONL dump.
	obsReg := observe.NewRegistry()
	if *debugListen != "" {
		go func() {
			if err := observe.ServeDebug(ctx, *debugListen, obsReg); err != nil {
				logger.Warn("debug http", "err", err)
			}
		}()
	}
	if *metricsDump != "" {
		go func() {
			if err := observe.NewDumper(obsReg, *metricsDump, *metricsInterval).Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
				logger.Warn("metrics dumper", "err", err)
			}
		}()
	}

	srv := edge.New(*listen, tlsConf, reg, policyEngine, policyCache, auditEm, pool, obsReg, logger)

	if err := srv.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		logger.Error("relay exited", "err", err)
		os.Exit(1)
	}
}

// intraTLS builds a client tls.Config from the relay's server config —
// same cert (which carries the relay's URI SAN), same CA pool. Used
// as the dial config for inter-relay connections.
func intraTLS(serverConf *tls.Config) *tls.Config {
	c := serverConf.Clone()
	c.ClientCAs = nil
	c.ClientAuth = 0
	c.RootCAs = serverConf.ClientCAs
	c.ServerName = "localhost"
	return c
}

func loadServerTLS(certPath, keyPath, caPath string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, err
	}
	// caPath comes from a flag wired by the operator, by design.
	caPEM, err := os.ReadFile(caPath) // #nosec G304
	if err != nil {
		return nil, err
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("tls: empty ca PEM")
	}
	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		ClientCAs:    pool,
		ClientAuth:   tls.RequireAndVerifyClientCert,
		MinVersion:   tls.VersionTLS13,
	}, nil
}

func newLogger(format string) *slog.Logger {
	var h slog.Handler
	if format == "json" {
		h = slog.NewJSONHandler(os.Stderr, nil)
	} else {
		h = slog.NewTextHandler(os.Stderr, nil)
	}
	return slog.New(h)
}

func signalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	sigC := make(chan os.Signal, 1)
	signal.Notify(sigC, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigC
		cancel()
	}()
	return ctx, cancel
}
