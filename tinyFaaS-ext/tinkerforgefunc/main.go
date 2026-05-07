package tinkerforgefunc

import (
	"log"

	"github.com/Tinkerforge/go-api-bindings/industrial_dual_relay_v2"
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

func (t *TinkerforgeController) PowerOn(channel int) error {
	ipcon := ipconnection.New()
	defer ipcon.Close()

	err := ipcon.Connect(t.host, t.port)
	if err != nil {
		return err
	}

	relay, err := industrial_dual_relay_v2.New(t.uid, ipcon)
	if err != nil {
		return err
	}

	log.Printf("Tinkerforge: powering ON channel %d", channel)

	switch channel {
	case 0:
		return relay.SetValue([2]bool{true, false})
	case 1:
		return relay.SetValue([2]bool{false, true})
	default:
		return relay.SetValue([2]bool{true, true})
	}
}

func (t *TinkerforgeController) PowerOff(channel int) error {
	ipcon := ipconnection.New()
	defer ipcon.Close()

	err := ipcon.Connect(t.host, t.port)
	if err != nil {
		return err
	}

	relay, err := industrial_dual_relay_v2.New(t.uid, ipcon)
	if err != nil {
		return err
	}

	log.Printf("Tinkerforge: powering OFF channel %d", channel)

	switch channel {
	case 0:
		return relay.SetValue([2]bool{false, false})
	case 1:
		return relay.SetValue([2]bool{false, false})
	default:
		return relay.SetValue([2]bool{false, false})
	}
}
