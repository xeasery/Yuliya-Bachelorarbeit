package tinkerforgefunc

import (
	"fmt"
	"log"

	"github.com/Tinkerforge/go-api-bindings/industrial_dual_relay_bricklet"
	"github.com/Tinkerforge/go-api-bindings/ipconnection"
)

type TinkerforgeController struct {
	host string
	port int
	uid  string
}

func NewTinkerforgeController(host string, port int, uid string) *TinkerforgeController {
	return &TinkerforgeController{
		host: host,
		port: port,
		uid:  uid,
	}
}

// setChannel connects to the relay and sets the given channel to on/off,
// leaving the other channel's current state untouched.
func (t *TinkerforgeController) setChannel(channel int, on bool) error {
	ipcon := ipconnection.New()
	defer ipcon.Close()

	err := ipcon.Connect(fmt.Sprintf("%s:%d", t.host, t.port))
	if err != nil {
		return err
	}

	relay, err := industrial_dual_relay_bricklet.New(t.uid, &ipcon)
	if err != nil {
		return err
	}

	c0, c1, err := relay.GetValue()
	if err != nil {
		return err
	}

	switch channel {
	case 0:
		c0 = on
	case 1:
		c1 = on
	default:
		return fmt.Errorf("invalid relay channel %d", channel)
	}

	return relay.SetValue(c0, c1)
}

func (t *TinkerforgeController) PowerOn(channel int) error {
	log.Printf("Tinkerforge: powering ON channel %d", channel)
	return t.setChannel(channel, true)
}

func (t *TinkerforgeController) PowerOff(channel int) error {
	log.Printf("Tinkerforge: powering OFF channel %d", channel)
	return t.setChannel(channel, false)
}
