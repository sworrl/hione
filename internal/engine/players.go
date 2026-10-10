package engine

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/sworrl/hione/internal/adb"
)

// Player is the device side: whatever plays the test file.
type Player interface {
	Name() string
	Prepare(local string) (handle string, err error)
	Play(handle string, startMs int64) error
	Stop() error
	SetDAC(adb.DAC) error
	DAC() (adb.DAC, error)
	CanSetDAC() bool
	LowerVolume() bool // one step down; false when it cannot
	Info() map[string]string
}

// ---- M500 over adb ----

type adbPlayer struct {
	d      *adb.Device
	pushed map[string]bool
}

func (p *adbPlayer) Name() string { return "M500 over adb (" + p.d.Label + ")" }

func (p *adbPlayer) Prepare(local string) (string, error) {
	if p.pushed == nil {
		p.pushed = map[string]bool{}
		if err := p.d.PrepareDir(); err != nil {
			return "", err
		}
	}
	remote := adb.DeviceDir + "/" + filepath.Base(local)
	if !p.pushed[remote] {
		if err := p.d.Push(local, remote); err != nil {
			return "", err
		}
		p.pushed[remote] = true
	}
	return remote, nil
}

// WiredOutput reports the wired output the M500's audio policy has, or "" when none is plugged.
func WiredOutput(d *adb.Device) string {
	out, _ := d.Shell(15*time.Second, "dumpsys media.audio_policy | grep -A400 'Available output devices' | grep -o 'AUDIO_DEVICE_OUT_[A-Z_]*' | sort -u")
	for _, l := range strings.Fields(out) {
		switch l {
		case "AUDIO_DEVICE_OUT_WIRED_HEADSET", "AUDIO_DEVICE_OUT_WIRED_HEADPHONE", "AUDIO_DEVICE_OUT_LINE":
			return strings.TrimPrefix(l, "AUDIO_DEVICE_OUT_")
		}
	}
	return ""
}

func (p *adbPlayer) Play(h string, startMs int64) error {
	if w := WiredOutput(p.d); w == "" {
		return errors.New("the M500 sees nothing in its headphone jack (no wired output in the audio policy), so it would play to nowhere. Push the plug fully into the jack")
	}
	return p.d.PlayFile(h, startMs)
}

func (p *adbPlayer) Stop() error {
	_, err := p.d.Stop()
	return err
}

func (p *adbPlayer) SetDAC(d adb.DAC) error { return p.d.SetDAC(d) }
func (p *adbPlayer) DAC() (adb.DAC, error)  { return p.d.DACState() }
func (p *adbPlayer) CanSetDAC() bool        { return true }

var volRe = regexp.MustCompile(`volume is (\d+) in range \[(\d+)\.\.(\d+)\]`)

func (p *adbPlayer) LowerVolume() bool {
	out, err := p.d.Shell(10*time.Second, "cmd media_session volume --stream 3 --get")
	if err != nil {
		return false
	}
	m := volRe.FindStringSubmatch(out)
	if m == nil {
		return false
	}
	v, _ := strconv.Atoi(m[1])
	mx, _ := strconv.Atoi(m[3])
	step := max(mx/25, 1) // about 4% of the range per step
	if v-step < 1 {
		return false
	}
	_, err = p.d.Shell(10*time.Second, fmt.Sprintf("cmd media_session volume --stream 3 --set %d", v-step))
	return err == nil
}

func (p *adbPlayer) Info() map[string]string {
	return map[string]string{
		"device":  "HiBy M500 / MikuOS",
		"adb":     p.d.Label,
		"build":   p.d.Model(),
		"volume":  p.d.Volume(),
		"output":  WiredOutput(p.d),
		"format":  p.d.NowPlayingFormat(),
	}
}

// ---- a person pressing play ----

type manualPlayer struct{ e *Engine }

func (p *manualPlayer) Name() string { return "manual (you press play)" }
func (p *manualPlayer) Prepare(local string) (string, error) {
	return local, nil
}
func (p *manualPlayer) Play(h string, startMs int64) error {
	msg := fmt.Sprintf("Play %s on the device now (from the start). Recording waits for its start marker.", filepath.Base(h))
	p.e.Logf("%s", msg)
	if p.e.Cfg.Prompt != nil {
		return p.e.Cfg.Prompt(msg)
	}
	return nil
}
func (p *manualPlayer) Stop() error { return nil }
func (p *manualPlayer) SetDAC(d adb.DAC) error {
	msg := fmt.Sprintf("Set the device to: %s", d)
	p.e.Logf("%s", msg)
	if p.e.Cfg.Prompt != nil {
		return p.e.Cfg.Prompt(msg + ", then continue.")
	}
	return nil
}
func (p *manualPlayer) DAC() (adb.DAC, error) { return adb.DAC{}, nil }
func (p *manualPlayer) CanSetDAC() bool       { return false }
func (p *manualPlayer) LowerVolume() bool     { return false }
func (p *manualPlayer) Info() map[string]string {
	return map[string]string{"device": "manual"}
}
