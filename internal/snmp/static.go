package snmp

import (
	"context"
	"errors"
	"net/netip"
	"slices"

	"github.com/maarten-boot/go-ined/internal/inventory"
	"github.com/maarten-boot/go-ined/pkg/apitypes"
)

// Static is a Client without a network, for tests of other packages: every
// address answers for Community with Version, with Sys and Vals.
type Static struct {
	Community, Version string
	Sys                inventory.SNMPSystem
	Vals               apitypes.SNMPValues
}

var errNoAnswer = errors.New("no answer")

// System implements Client.
func (s Static) System(_ context.Context, _ netip.Addr, community, version string) (inventory.SNMPSystem, error) {
	if community != s.Community || version != s.Version {
		return inventory.SNMPSystem{}, errNoAnswer
	}
	return s.Sys, nil
}

// Values implements Client.
func (s Static) Values(_ context.Context, _ netip.Addr, community, version string) (apitypes.SNMPValues, error) {
	if community != s.Community || version != s.Version {
		return apitypes.SNMPValues{}, errNoAnswer
	}
	v := s.Vals
	v.Storage = slices.Clone(v.Storage)
	return v, nil
}
