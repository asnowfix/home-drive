// Package syncer implements the push/pull sync engine with conflict
// resolution, exponential backoff retry, and bisync safety net.
//
// The Puller polls the Google Drive Changes API at a configurable
// interval, downloads remote modifications, runs conflict resolution
// when local and remote state diverge, and emits MQTT events and audit
// log entries for every operation.
//
// All external dependencies (RemoteFS, Store, Publisher) are expressed
// as local interfaces so the package compiles and tests in isolation.
package syncer
