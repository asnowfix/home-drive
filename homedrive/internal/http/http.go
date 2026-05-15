// Package http provides the loopback HTTP control endpoint for homedrive,
// serving /status, /pause, /resume, /resync, /reload, /healthz, and /metrics.
//
// The server accepts component interfaces (Pausable, Resyncable, Reloadable,
// StatusProvider, HealthChecker) so it can control the agent without depending
// on concrete implementations.
//
// Typical usage:
//
//	srv := http.NewServer(cfg, deps, metrics, logger)
//	go srv.ListenAndServe()
//	// ... on shutdown:
//	srv.Shutdown(ctx)
package http
