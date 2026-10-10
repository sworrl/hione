package engine

import (
	"sync"
	"time"

	"github.com/sworrl/hione/internal/adb"
)

// DeviceStatus is the last thing the poller read from the device, with where each value came from.
type DeviceStatus struct {
	Target   string    `json:"target"` // the adb transport in use
	OK       bool      `json:"ok"`     // false: adb lost
	Error    string    `json:"error,omitempty"`
	Updated  time.Time `json:"updated"`
	DAC      *adb.DAC  `json:"dac,omitempty"`
	DACLabel string    `json:"dac_label,omitempty"`
	Wired    string    `json:"wired_output"`
	Volume   string    `json:"volume"`
	Sources  map[string]string `json:"sources"`
}

type devPoller struct {
	mu sync.Mutex
	st DeviceStatus
}

// DevStatus returns the cached device status (nil without adb).
func (e *Engine) DevStatus() *DeviceStatus {
	if e.Dev == nil {
		return nil
	}
	e.poll.mu.Lock()
	defer e.poll.mu.Unlock()
	s := e.poll.st
	return &s
}

func (e *Engine) pollDevice() {
	full := true
	for {
		st := DeviceStatus{Target: e.Dev.Label, Updated: time.Now(), Sources: map[string]string{
			"dac":    "settings get global miku_dac_state",
			"wired":  "dumpsys media.audio_policy (Available output devices)",
			"volume": "cmd media_session volume --stream 3 --get",
		}}
		d, err := e.Dev.DACState()
		if err != nil {
			st.OK = false
			st.Error = "adb lost: " + err.Error()
		} else {
			st.OK = true
			st.DAC = &d
			st.DACLabel = d.String()
			// The heavier reads only when no run is using the link.
			e.poll.mu.Lock()
			prev := e.poll.st
			e.poll.mu.Unlock()
			if full || !e.Busy() {
				st.Wired = WiredOutput(e.Dev)
				if st.Wired == "" {
					st.Wired = "none"
				}
				st.Volume = e.Dev.Volume()
			} else {
				st.Wired, st.Volume = prev.Wired, prev.Volume
			}
			full = false
		}
		e.poll.mu.Lock()
		was := e.poll.st.OK
		e.poll.st = st
		e.poll.mu.Unlock()
		if was && !st.OK {
			e.Logf("adb lost: %s", st.Error)
		} else if !was && st.OK && !st.Updated.IsZero() {
			e.Logf("adb connected: %s, DAC %s, output %s, volume %s.", st.Target, st.DACLabel, st.Wired, st.Volume)
		}
		time.Sleep(10 * time.Second)
	}
}
