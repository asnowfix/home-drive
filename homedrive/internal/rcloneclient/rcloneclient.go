// Package rcloneclient wraps the minimal set of rclone library calls needed
// for Google Drive operations. Only backend/drive is imported to keep the
// binary under 25 MB. All remote filesystem access goes through the
// RemoteFS interface so tests can use MemFS or FlakyFS.
package rcloneclient
