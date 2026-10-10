// Package adb drives a MikuOS device (HiBy M500) over adb: it pushes test files, plays them
// through Miku Music, sets the DAC through Miku Music's shell commands and reads back
// Settings.Global miku_dac_state.
package adb

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// Device is one adb transport.
type Device struct {
	args  []string // -s serial or -t id
	Label string
}

// DeviceDir is where test files go on the device. It is under Music so Miku Music (which has
// all-files access) can read it; a .nomedia file keeps it out of the library.
const DeviceDir = "/sdcard/Music/mikumeasure"

var devLine = regexp.MustCompile(`^(\S+)\s+(\S+)\s+(.*)$`)

// Find returns the adb transport to use. want may be a serial, "t:<transport id>", or empty to
// pick the first MikuOS device that answers (network transports first: measurements need the
// USB cable out to avoid a ground loop).
func Find(want string) (*Device, error) {
	if _, err := exec.LookPath("adb"); err != nil {
		return nil, errors.New("adb not found in PATH")
	}
	if want != "" {
		var d *Device
		if strings.HasPrefix(want, "t:") {
			d = &Device{args: []string{"-t", strings.TrimPrefix(want, "t:")}, Label: want}
		} else {
			d = &Device{args: []string{"-s", want}, Label: want}
		}
		if _, err := d.Shell(5*time.Second, "echo ok"); err != nil {
			return nil, fmt.Errorf("adb %s: %w", want, err)
		}
		return d, nil
	}
	out, err := exec.Command("adb", "devices", "-l").Output()
	if err != nil {
		return nil, err
	}
	var cands []*Device
	for _, l := range strings.Split(string(out), "\n") {
		m := devLine.FindStringSubmatch(strings.TrimSpace(l))
		if m == nil || m[2] != "device" || !strings.Contains(m[3], "mikuOS") {
			continue
		}
		var d *Device
		if tid := regexp.MustCompile(`transport_id:(\d+)`).FindStringSubmatch(m[3]); tid != nil {
			d = &Device{args: []string{"-t", tid[1]}, Label: m[1] + " (t:" + tid[1] + ")"}
		} else {
			d = &Device{args: []string{"-s", m[1]}, Label: m[1]}
		}
		if strings.Contains(m[1], ":") {
			cands = append([]*Device{d}, cands...)
		} else {
			cands = append(cands, d)
		}
	}
	for _, d := range cands {
		if _, err := d.Shell(5*time.Second, "echo ok"); err == nil {
			return d, nil
		}
	}
	return nil, errors.New("no MikuOS device answers on adb (adb connect <ip>:5555 for Wi-Fi)")
}

// MinTimeout is the least time any adb command gets: Wi-Fi adb sometimes stalls for 15 s or
// more while the device's radio wakes up.
var MinTimeout = 45 * time.Second

// Shell runs a command on the device, once more if the first try times out.
func (d *Device) Shell(timeout time.Duration, cmd string) (string, error) {
	if timeout < MinTimeout {
		timeout = MinTimeout
	}
	var out []byte
	var err error
	for try := 0; try < 2; try++ {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		args := append(append([]string{}, d.args...), "shell", cmd)
		out, err = exec.CommandContext(ctx, "adb", args...).CombinedOutput()
		timedOut := ctx.Err() != nil
		cancel()
		if err == nil {
			return strings.TrimRight(string(out), "\r\n"), nil
		}
		if !timedOut {
			break
		}
	}
	return string(out), fmt.Errorf("adb shell %q: %v: %s", cmd, err, strings.TrimSpace(string(out)))
}

// Push copies a file to the device.
func (d *Device) Push(local, remote string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	args := append(append([]string{}, d.args...), "push", local, remote)
	out, err := exec.CommandContext(ctx, "adb", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("adb push: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Pull copies a file from the device.
func (d *Device) Pull(remote, local string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	args := append(append([]string{}, d.args...), "pull", remote, local)
	out, err := exec.CommandContext(ctx, "adb", args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("adb pull: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// PrepareDir makes the device folder and its .nomedia.
func (d *Device) PrepareDir() error {
	_, err := d.Shell(10*time.Second, "mkdir -p "+DeviceDir+" && touch "+DeviceDir+"/.nomedia")
	return err
}

// DAC is the DAC state as miku_dac_state reports it.
type DAC struct {
	Filter string `json:"filter"`
	Gain   string `json:"gain"`
	DRE    string `json:"dre"` // dremode_enable / dremode_disable
	HP     string `json:"hp"`  // hpower_enable / hpower_disable
}

// String is a short label like "nos, gain high, DRE on, HP off".
func (s DAC) String() string {
	return fmt.Sprintf("%s, gain %s, DRE %s, HP %s", FilterLabel(s.Filter), s.Gain, onoff(s.DRE == "dremode_enable"), onoff(s.HP == "hpower_enable"))
}

func onoff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

// Filters in the order the matrix runs them.
var Filters = []string{"fast_rolloff_low_latency", "fast_rolloff_phase_compensated",
	"slow_rolloff_low_latency", "slow_rolloff_phase_compensated", "nos"}

// FilterLabel turns the kernel name into a short label.
func FilterLabel(f string) string {
	switch f {
	case "fast_rolloff_low_latency":
		return "Fast, low latency"
	case "fast_rolloff_phase_compensated":
		return "Fast, phase compensated"
	case "slow_rolloff_low_latency":
		return "Slow, low latency"
	case "slow_rolloff_phase_compensated":
		return "Slow, phase compensated"
	case "nos":
		return "NOS"
	case "":
		return "unknown"
	}
	return f
}

// ParseDAC parses "filter=nos;gain=high;dre=dremode_enable;hp=hpower_enable".
func ParseDAC(s string) DAC {
	var d DAC
	for _, kv := range strings.Split(strings.TrimSpace(s), ";") {
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case "filter":
			d.Filter = v
		case "gain":
			d.Gain = v
		case "dre":
			d.DRE = v
		case "hp":
			d.HP = v
		}
	}
	return d
}

// DACState reads miku_dac_state.
func (d *Device) DACState() (DAC, error) {
	out, err := d.Shell(10*time.Second, "settings get global miku_dac_state")
	if err != nil {
		return DAC{}, err
	}
	return ParseDAC(out), nil
}

var resultRe = regexp.MustCompile(`result=(-?\d+)(?:, data="(.*)")?`)

// Broadcast sends an action to Miku Music's shell receiver and returns its result data.
func (d *Device) Broadcast(action string, extras ...string) (string, error) {
	cmd := "am broadcast -p com.miku.player -a com.miku.player.action." + action
	for i := 0; i+1 < len(extras); i += 2 {
		cmd += fmt.Sprintf(" %s %s", extras[i], shq(extras[i+1]))
	}
	out, err := d.Shell(15*time.Second, cmd)
	if err != nil {
		return "", err
	}
	m := resultRe.FindStringSubmatch(out)
	if m == nil {
		return "", fmt.Errorf("%s: no result from Miku Music: %s", action, out)
	}
	if m[1] == "0" && m[2] == "" {
		return "", fmt.Errorf("%s: Miku Music did not handle it (needs the build with the mikumeasure shell commands)", action)
	}
	if m[1] != "1" {
		return m[2], fmt.Errorf("%s: %s", action, m[2])
	}
	return m[2], nil
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// SetDAC sends the knobs that differ from want and waits until miku_dac_state shows them.
func (d *Device) SetDAC(want DAC) error {
	var ex []string
	if want.Filter != "" {
		ex = append(ex, "--es digital_filter", want.Filter)
	}
	if want.Gain != "" {
		ex = append(ex, "--es gain", want.Gain)
	}
	if want.DRE != "" {
		ex = append(ex, "--es dre_mode", want.DRE)
	}
	if want.HP != "" {
		ex = append(ex, "--es high_power", want.HP)
	}
	if _, err := d.Broadcast("DAC_SET", ex...); err != nil {
		return err
	}
	deadline := time.Now().Add(6 * time.Second)
	var got DAC
	for time.Now().Before(deadline) {
		var err error
		got, err = d.DACState()
		if err == nil && matches(got, want) {
			return nil
		}
		time.Sleep(250 * time.Millisecond)
	}
	return fmt.Errorf("DAC did not take the setting: asked %s, miku_dac_state says %s", want, got)
}

func matches(got, want DAC) bool {
	return (want.Filter == "" || got.Filter == want.Filter) && (want.Gain == "" || got.Gain == want.Gain) &&
		(want.DRE == "" || got.DRE == want.DRE) && (want.HP == "" || got.HP == want.HP)
}

// Matches reports whether got has every knob want sets.
func Matches(got, want DAC) bool { return matches(got, want) }

// PlayFile starts a file on the device through Miku Music.
func (d *Device) PlayFile(remote string, startMs int64) error {
	_, err := d.Broadcast("PLAY_FILE", "--es path", remote, "--el start_ms", fmt.Sprint(startMs), "--ez eq_off", "true")
	return err
}

// Stop pauses and puts the user's queue back.
func (d *Device) Stop() (string, error) { return d.Broadcast("MEASURE_STOP") }

// Status returns Miku Music's player status line.
func (d *Device) Status() (string, error) { return d.Broadcast("MEASURE_STATUS") }

// Volume returns the music stream volume as "index/max" when it can be read.
func (d *Device) Volume() string {
	out, err := d.Shell(10*time.Second, "cmd media_session volume --stream 3 --get 2>/dev/null")
	if err != nil {
		return ""
	}
	if m := regexp.MustCompile(`volume is (\d+) in range \[(\d+)\.\.(\d+)\]`).FindStringSubmatch(out); m != nil {
		return m[1] + "/" + m[3]
	}
	return ""
}

// NowPlayingFormat reads what Miku Music says the output format is.
func (d *Device) NowPlayingFormat() string {
	out, _ := d.Shell(10*time.Second, "settings get global miku_now_playing_format")
	if out == "null" {
		return ""
	}
	return out
}

// Model returns the build id (for the report).
func (d *Device) Model() string {
	out, _ := d.Shell(10*time.Second, "getprop ro.build.display.id")
	return out
}
