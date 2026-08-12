package tinkerforgefunc

import (
	"fmt"
	"log"

	"github.com/Tinkerforge/go-api-bindings/industrial_dual_relay_bricklet"
	"github.com/Tinkerforge/go-api-bindings/ipconnection"
)

// ChannelsPerRelay is how many nodes one Industrial Dual Relay Bricklet can
// switch. A cluster with more workers than this needs additional bricklets,
// addressed by UID.
const ChannelsPerRelay = 2

type TinkerforgeController struct {
	host string
	port int
	// defaultUID is used for nodes that do not name a relay of their own,
	// so a single-relay cluster needs no per-node configuration.
	defaultUID string
}

func NewTinkerforgeController(host string, port int, defaultUID string) *TinkerforgeController {
	return &TinkerforgeController{
		host:       host,
		port:       port,
		defaultUID: defaultUID,
	}
}

// setChannel connects to the given relay and sets one channel on or off,
// leaving the other channel's current state untouched.
//
// The connection is opened per call rather than held open: power changes are
// rare, and a long-lived connection to a bricklet that may be unplugged
// between runs is more failure than it is worth.
func (t *TinkerforgeController) setChannel(relayUID string, channel int, on bool) error {
	if relayUID == "" {
		relayUID = t.defaultUID
	}

	if channel < 0 || channel >= ChannelsPerRelay {
		return fmt.Errorf("invalid relay channel %d for relay %s: an Industrial Dual Relay Bricklet has channels 0-%d",
			channel, relayUID, ChannelsPerRelay-1)
	}

	ipcon := ipconnection.New()
	defer ipcon.Close()

	if err := ipcon.Connect(fmt.Sprintf("%s:%d", t.host, t.port)); err != nil {
		return fmt.Errorf("connect to brickd at %s:%d: %w", t.host, t.port, err)
	}

	relay, err := industrial_dual_relay_bricklet.New(relayUID, &ipcon)
	if err != nil {
		return fmt.Errorf("open relay %s: %w", relayUID, err)
	}

	// Read-modify-write: the other channel belongs to a different node and
	// must not be disturbed by switching this one.
	c0, c1, err := relay.GetValue()
	if err != nil {
		return fmt.Errorf("read relay %s state: %w", relayUID, err)
	}

	switch channel {
	case 0:
		c0 = on
	case 1:
		c1 = on
	}

	if err := relay.SetValue(c0, c1); err != nil {
		return fmt.Errorf("set relay %s channel %d: %w", relayUID, channel, err)
	}

	return nil
}

func (t *TinkerforgeController) PowerOn(relayUID string, channel int) error {
	log.Printf("Tinkerforge: powering ON relay %s channel %d", relayOrDefault(relayUID, t.defaultUID), channel)
	return t.setChannel(relayUID, channel, true)
}

func (t *TinkerforgeController) PowerOff(relayUID string, channel int) error {
	log.Printf("Tinkerforge: powering OFF relay %s channel %d", relayOrDefault(relayUID, t.defaultUID), channel)
	return t.setChannel(relayUID, channel, false)
}

func relayOrDefault(uid, fallback string) string {
	if uid == "" {
		return fallback
	}
	return uid
}
