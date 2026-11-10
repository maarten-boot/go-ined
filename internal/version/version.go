// Package version holds the build version, set at link time:
//
//	go build -ldflags "-X github.com/maarten-boot/go-ined/internal/version.Version=v0.1.0"
package version

// Version is the program version. "dev" for untagged builds.
var Version = "dev"
