package report

import (
	"strings"

	"github.com/sworrl/hione/internal/engine"
)

// Reports and charts get published, so they leave out where the gear sits on the network:
// the ssh host of the capture card and the adb address of the device. run.json keeps both.

// publicCapture is the capture URI without an ssh host: ssh://host/hw:0,0 becomes
// "hw:0,0 over ssh".
func publicCapture(uri string) string {
	if !strings.HasPrefix(uri, "ssh://") {
		return uri
	}
	rest := strings.TrimPrefix(uri, "ssh://")
	dev := "hw:0,0"
	if i := strings.IndexByte(rest, '/'); i >= 0 && i+1 < len(rest) {
		dev = rest[i+1:]
	}
	return dev + " over ssh"
}

// publicDevice drops a trailing "(address)" that older runs put after the device name.
func publicDevice(d string) string {
	if i := strings.Index(d, " ("); i > 0 && strings.HasSuffix(d, ")") {
		return d[:i]
	}
	return d
}

// publicDeviceKeys are the run.json device fields that go into reports.
func publicDeviceKeys(r *engine.Run) []string {
	var ks []string
	for _, k := range sortedKeys(r.Device) {
		if k != "adb" && k != "serial" {
			ks = append(ks, k)
		}
	}
	return ks
}

func deviceField(r *engine.Run, k string) string {
	if k == "device" {
		return publicDevice(r.Device[k])
	}
	return r.Device[k]
}
