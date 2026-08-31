package tinkerforgefunc

import (
	"fmt"
	"log"
	"sync"

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

	// The bricklet's API sets both channels in one call, so switching one
	// node requires supplying the other's state too. Reading it back from
	// the hardware each time looks natural and is wrong: two nodes sharing a
	// relay produce two read-modify-write cycles, and the second read can
	// still observe the state from before the first write. It then writes
	// the stale value back and silently re-energises a node that was just
	// switched off -- while both operations report success.
	//
	// That is not a theoretical race. It left nodes powered and drawing
	// while the registry recorded them asleep, which is precisely the error
	// a power-measurement study cannot afford, and it only affected nodes
	// that shared a relay.
	//
	// So desired state is authoritative and lives here, guarded by the
	// mutex. Hardware is written from it, never read to build it.
	mu    sync.Mutex
	state map[string]*[ChannelsPerRelay]bool
}

func NewTinkerforgeController(host string, port int, defaultUID string) *TinkerforgeController {
	return &TinkerforgeController{
		host:       host,
		port:       port,
		defaultUID: defaultUID,
		state:      make(map[string]*[ChannelsPerRelay]bool),
	}
}

// setChannel sets one channel of one relay, leaving the other channel at
// whatever this controller last set it to.
//
// The whole operation is serialised: concurrent wakes of two nodes on the
// same relay would otherwise interleave their writes.
func (t *TinkerforgeController) setChannel(relayUID string, channel int, on bool) error {
	if relayUID == "" {
		relayUID = t.defaultUID
	}

	if channel < 0 || channel >= ChannelsPerRelay {
		return fmt.Errorf("invalid relay channel %d for relay %s: an Industrial Dual Relay Bricklet has channels 0-%d",
			channel, relayUID, ChannelsPerRelay-1)
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	desired, known := t.state[relayUID]
	if !known {
		// First time this relay is touched. Adopt the hardware's current
		// state so the channel we are not changing keeps its position --
		// this is the only read, and nothing else is in flight yet.
		initial, err := t.readState(relayUID)
		if err != nil {
			return err
		}
		desired = &initial
		t.state[relayUID] = desired
	}

	next := *desired
	next[channel] = on

	if err := t.writeState(relayUID, next); err != nil {
		return err
	}

	*desired = next
	return nil
}

// withRelay opens a connection, hands the relay to fn, and closes up after.
// The connection is per call rather than long-lived: power changes are rare,
// and a held-open connection to a bricklet that may be unplugged between runs
// is more failure than it is worth.
//
// IPConnection carries a mutex, so it is only ever passed by pointer.
func (t *TinkerforgeController) withRelay(
	relayUID string,
	fn func(*industrial_dual_relay_bricklet.IndustrialDualRelayBricklet) error,
) error {
	ipcon := ipconnection.New()
	defer ipcon.Close()

	if err := ipcon.Connect(fmt.Sprintf("%s:%d", t.host, t.port)); err != nil {
		return fmt.Errorf("connect to brickd at %s:%d: %w", t.host, t.port, err)
	}

	relay, err := industrial_dual_relay_bricklet.New(relayUID, &ipcon)
	if err != nil {
		return fmt.Errorf("open relay %s: %w", relayUID, err)
	}

	return fn(&relay)
}

func (t *TinkerforgeController) readState(relayUID string) ([ChannelsPerRelay]bool, error) {
	var out [ChannelsPerRelay]bool

	err := t.withRelay(relayUID, func(relay *industrial_dual_relay_bricklet.IndustrialDualRelayBricklet) error {
		c0, c1, err := relay.GetValue()
		if err != nil {
			return fmt.Errorf("read relay %s state: %w", relayUID, err)
		}
		out[0], out[1] = c0, c1
		return nil
	})

	return out, err
}

func (t *TinkerforgeController) writeState(relayUID string, s [ChannelsPerRelay]bool) error {
	return t.withRelay(relayUID, func(relay *industrial_dual_relay_bricklet.IndustrialDualRelayBricklet) error {
		if err := relay.SetValue(s[0], s[1]); err != nil {
			return fmt.Errorf("set relay %s to (%v, %v): %w", relayUID, s[0], s[1], err)
		}
		return nil
	})
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
