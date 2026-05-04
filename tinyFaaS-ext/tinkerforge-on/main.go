package tinkerforgefunc

import "strings"

func fn(body string, headers map[string]string) (string, error) {

	cmd := strings.ToLower(strings.TrimSpace(body))

	switch cmd {
	case "on":
		//TODO
		return "turned on", nil

	case "off":
		//TODO
		return "turned off", nil

	default:
		return "unknown command, use on/off", nil
	}
}
