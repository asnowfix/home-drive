// Package rcloneclient wraps the minimal set of rclone library calls needed
// for Google Drive operations. Only backend/drive is imported to keep the
// binary under 25 MB. All remote filesystem access goes through the
// RemoteFS interface so tests can use MemFS or FlakyFS.
//
// This package is the ONLY package in homedrive allowed to import rclone.
// Everything else uses the RemoteFS interface defined here.
//
// Implementations:
//   - RcloneFS: production, wraps rclone/operations + rclone/fs.
//   - MemFS: in-memory thread-safe, for unit tests.
//   - FlakyFS: decorator injecting errors/latency/timeouts, for robustness tests.
//   - DryRunFS: logs intended writes, delegates reads; for --dry-run mode.
package rcloneclient
