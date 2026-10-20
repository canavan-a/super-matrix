// Package version is the build version of the server and relay, overridden
// at build time with -ldflags "-X main/version.Version=…".
package version

var Version = "0.1.0"
