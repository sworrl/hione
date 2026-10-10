package capture

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

func openFile(p string) (*os.File, error) { return os.Open(p) }

// Device is one capture or playback endpoint.
type Device struct {
	URI         string `json:"uri"`
	Direction   string `json:"direction"` // capture, playback
	Description string `json:"description"`
	Backend     string `json:"backend"`
}

var alsaLine = regexp.MustCompile(`^card (\d+): ([^ ]+) \[([^\]]*)\], device (\d+): ([^\[]*)\[([^\]]*)\]`)

func parseAlsaList(out, dir, prefix, backend string) []Device {
	var ds []Device
	for _, l := range strings.Split(out, "\n") {
		m := alsaLine.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		ds = append(ds, Device{
			URI:         fmt.Sprintf("%shw:%s,%s", prefix, m[1], m[4]),
			Direction:   dir,
			Description: strings.TrimSpace(m[3] + ", " + m[6]),
			Backend:     backend,
		})
	}
	return ds
}

// List returns local ALSA and PipeWire devices and, for each ssh host given, that host's ALSA
// devices. Missing tools are skipped; problems are returned as notes.
func List(sshHosts []string) ([]Device, []string) {
	var ds []Device
	var notes []string
	if _, err := exec.LookPath("arecord"); err == nil {
		out, _ := exec.Command("arecord", "-l").CombinedOutput()
		got := parseAlsaList(string(out), "capture", "alsa:", "alsa")
		if len(got) == 0 {
			notes = append(notes, "ALSA: no local capture cards")
		}
		ds = append(ds, got...)
		out, _ = exec.Command("aplay", "-l").CombinedOutput()
		ds = append(ds, parseAlsaList(string(out), "playback", "alsa:", "alsa")...)
	}
	if _, err := exec.LookPath("pw-dump"); err == nil {
		out, err := exec.Command("pw-dump").Output()
		if err == nil {
			var objs []struct {
				Type string `json:"type"`
				Info struct {
					Props map[string]any `json:"props"`
				} `json:"info"`
			}
			if json.Unmarshal(out, &objs) == nil {
				for _, o := range objs {
					if o.Type != "PipeWire:Interface:Node" {
						continue
					}
					cls, _ := o.Info.Props["media.class"].(string)
					name, _ := o.Info.Props["node.name"].(string)
					desc, _ := o.Info.Props["node.description"].(string)
					switch cls {
					case "Audio/Source":
						ds = append(ds, Device{URI: "pw:" + name, Direction: "capture", Description: desc, Backend: "pipewire"})
					case "Audio/Sink":
						ds = append(ds, Device{URI: "pw:" + name, Direction: "playback", Description: desc, Backend: "pipewire"})
					}
				}
			}
		}
	}
	for _, h := range sshHosts {
		out, err := exec.Command("ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5", h, "arecord -l; echo ===; aplay -l").CombinedOutput()
		if err != nil {
			notes = append(notes, fmt.Sprintf("ssh %s: %v", h, err))
			continue
		}
		parts := strings.SplitN(string(out), "===", 2)
		ds = append(ds, parseAlsaList(parts[0], "capture", "ssh://"+h+"/", "ssh")...)
		if len(parts) > 1 {
			ds = append(ds, parseAlsaList(parts[1], "playback", "ssh://"+h+"/", "ssh")...)
		}
	}
	ds = append(ds, Device{URI: "sim", Direction: "capture", Description: "simulated loopback (synthetic device model)", Backend: "sim"})
	return ds, notes
}
