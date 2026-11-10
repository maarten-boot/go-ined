package config

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
)

// IsLoopbackListen reports whether a listen address ("host:port") binds to
// loopback only. An empty host, 0.0.0.0, :: and host names other than
// "localhost" count as non-loopback.
func IsLoopbackListen(addr string) (bool, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false, fmt.Errorf("server.listen %q: %w", addr, err)
	}
	if strings.EqualFold(host, "localhost") {
		return true, nil
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false, nil
	}
	return ip.IsLoopback(), nil
}

// CheckListenSecurity enforces SPEC-802: a non-loopback listener requires a
// token, otherwise the server must refuse to start.
func CheckListenSecurity(c Server) error {
	lo, err := IsLoopbackListen(c.Server.Listen)
	if err != nil {
		return err
	}
	if !lo && c.Server.Token == "" {
		return errors.New("server.listen " + c.Server.Listen +
			" is not a loopback address: set server.token (SPEC-802)")
	}
	return nil
}
