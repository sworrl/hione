// Package capture records and plays audio through command-line tools: arecord/aplay (ALSA),
// pw-record/pw-play (PipeWire), and the same ALSA tools on another machine over ssh. Samples
// stream as raw interleaved little-endian S32 (or S16/F32) on the tool's stdout.
package capture

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/sworrl/hione/internal/wav"
)

// Spec says what to capture.
type Spec struct {
	URI      string // alsa:hw:1,0  pw:<node>  ssh://host/hw:0,0  sim:  (empty = auto)
	Rate     int
	Channels int
	Format   wav.Format // S16, S32 or F32 on the wire
}

// Backend kinds.
const (
	KindALSA = "alsa"
	KindPW   = "pw"
	KindSSH  = "ssh"
	KindSim  = "sim"
)

// Parsed is a capture URI split into its parts.
type Parsed struct {
	Kind   string
	Host   string // ssh only
	Device string // hw:0,0, a PipeWire node name, or "" for default
}

// Parse splits a capture URI.
func Parse(uri string) (Parsed, error) {
	switch {
	case uri == "" || uri == "auto":
		if _, err := exec.LookPath("pw-record"); err == nil {
			return Parsed{Kind: KindPW}, nil
		}
		if _, err := exec.LookPath("arecord"); err == nil {
			return Parsed{Kind: KindALSA, Device: "default"}, nil
		}
		return Parsed{}, errors.New("no capture tool found (install pipewire or alsa-utils)")
	case strings.HasPrefix(uri, "ssh://"):
		u, err := url.Parse(uri)
		if err != nil {
			return Parsed{}, err
		}
		dev := strings.TrimPrefix(u.Path, "/")
		if dev == "" {
			dev = "hw:0,0"
		}
		host := u.Host
		if u.User != nil {
			host = u.User.String() + "@" + host
		}
		return Parsed{Kind: KindSSH, Host: host, Device: dev}, nil
	case strings.HasPrefix(uri, "alsa:"):
		return Parsed{Kind: KindALSA, Device: strings.TrimPrefix(uri, "alsa:")}, nil
	case strings.HasPrefix(uri, "pw:"):
		return Parsed{Kind: KindPW, Device: strings.TrimPrefix(uri, "pw:")}, nil
	case strings.HasPrefix(uri, "sim"):
		return Parsed{Kind: KindSim}, nil
	case strings.HasPrefix(uri, "hw:") || strings.HasPrefix(uri, "plughw:") || uri == "default":
		return Parsed{Kind: KindALSA, Device: uri}, nil
	}
	return Parsed{}, fmt.Errorf("unknown capture %q (use alsa:hw:1,0, pw:<node>, ssh://host/hw:0,0 or sim)", uri)
}

func alsaFormat(f wav.Format) string {
	switch f {
	case wav.S16:
		return "S16_LE"
	case wav.F32:
		return "FLOAT_LE"
	case wav.S24:
		return "S24_3LE"
	}
	return "S32_LE"
}

// CardOf returns the card part of hw:C,D (or "0").
func CardOf(dev string) string {
	d := strings.TrimPrefix(strings.TrimPrefix(dev, "plughw:"), "hw:")
	if i := strings.IndexByte(d, ','); i >= 0 {
		d = d[:i]
	}
	if d == "" {
		return "0"
	}
	return d
}

// Command builds the recording command line for p.
func Command(p Parsed, s Spec) (string, []string) {
	switch p.Kind {
	case KindSSH:
		remote := fmt.Sprintf("exec arecord -q -D %s -f %s -r %d -c %d -t raw", shq(p.Device), alsaFormat(s.Format), s.Rate, s.Channels)
		return "ssh", []string{"-o", "BatchMode=yes", "-o", "ServerAliveInterval=5", p.Host, remote}
	case KindPW:
		args := []string{"--rate", fmt.Sprint(s.Rate), "--channels", fmt.Sprint(s.Channels), "--format", strings.ToLower(string(s.Format))}
		if p.Device != "" {
			args = append(args, "--target", p.Device)
		}
		return "pw-record", append(args, "-")
	default:
		return "arecord", []string{"-q", "-D", p.Device, "-f", alsaFormat(s.Format), "-r", fmt.Sprint(s.Rate), "-c", fmt.Sprint(s.Channels), "-t", "raw"}
	}
}

func shq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// Stream is a running capture. Frames arrive on C as blocks of [channel][frame] float64.
type Stream struct {
	C      chan [][]float64
	cmd    *exec.Cmd
	cancel context.CancelFunc
	stderr *bytes.Buffer
	mu     sync.Mutex
	err    error
	done   chan struct{}
}

// Err returns the error that ended the stream (with the tool's stderr), if any.
func (s *Stream) Err() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// Done is closed when the stream ends.
func (s *Stream) Done() <-chan struct{} { return s.done }

// Stop ends the capture.
func (s *Stream) Stop() {
	s.cancel()
	<-s.done
}

// Start launches the capture tool. blockFrames sets the block size sent on C.
func Start(ctx context.Context, s Spec, blockFrames int) (*Stream, error) {
	p, err := Parse(s.URI)
	if err != nil {
		return nil, err
	}
	if p.Kind == KindSim {
		return nil, errors.New("sim capture is started by the simulator")
	}
	name, args := Command(p, s)
	cctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(cctx, name, args...)
	st := &Stream{C: make(chan [][]float64, 64), cmd: cmd, cancel: cancel, stderr: &bytes.Buffer{}, done: make(chan struct{})}
	cmd.Stderr = &lockedWriter{w: st.stderr, mu: &st.mu}
	out, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	go st.pump(bufio.NewReaderSize(out, 1<<20), s, blockFrames, cctx)
	return st, nil
}

type lockedWriter struct {
	w  io.Writer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(b)
}

func (st *Stream) pump(r io.Reader, s Spec, blockFrames int, ctx context.Context) {
	defer close(st.done)
	defer close(st.C)
	bps := s.Format.Bits() / 8
	bpf := bps * s.Channels
	buf := make([]byte, blockFrames*bpf)
	var rerr error
	for {
		_, err := io.ReadFull(r, buf)
		if err != nil {
			rerr = err
			break
		}
		blk := make([][]float64, s.Channels)
		for c := range blk {
			blk[c] = make([]float64, blockFrames)
		}
		for i := 0; i < blockFrames; i++ {
			for c := 0; c < s.Channels; c++ {
				blk[c][i] = decode(buf[i*bpf+c*bps:], s.Format)
			}
		}
		select {
		case st.C <- blk:
		case <-ctx.Done():
			rerr = ctx.Err()
		}
		if rerr != nil {
			break
		}
	}
	werr := st.cmd.Wait()
	st.mu.Lock()
	defer st.mu.Unlock()
	if ctx.Err() == nil {
		msg := strings.TrimSpace(st.stderr.String())
		if msg == "" && werr != nil {
			msg = werr.Error()
		}
		if msg == "" && rerr != nil {
			msg = rerr.Error()
		}
		if msg == "" {
			msg = "capture ended"
		}
		st.err = fmt.Errorf("capture stopped: %s", msg)
	}
}

func decode(p []byte, f wav.Format) float64 {
	le := binary.LittleEndian
	switch f {
	case wav.S16:
		return float64(int16(le.Uint16(p))) / 32768
	case wav.F32:
		return float64(math.Float32frombits(le.Uint32(p)))
	case wav.S24:
		return float64(int32(uint32(p[0])<<8|uint32(p[1])<<16|uint32(p[2])<<24)>>8) / 8388608
	}
	return float64(int32(le.Uint32(p))) / 2147483648
}

// Mixer setup for ssh and local ALSA cards: line input, no boost, capture at 0 dB. The previous
// state is saved with alsactl and put back by Restore.
type Mixer struct {
	p     Parsed
	saved string
}

func run(p Parsed, script string) (string, error) {
	var cmd *exec.Cmd
	if p.Kind == KindSSH {
		cmd = exec.Command("ssh", "-o", "BatchMode=yes", p.Host, script)
	} else {
		cmd = exec.Command("sh", "-c", script)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// SetupLineIn saves the card's mixer and sets it for a clean line capture. Controls that do not
// exist on a card are skipped. Returns a Mixer whose Restore puts the saved state back.
func SetupLineIn(p Parsed) (*Mixer, string, error) {
	if p.Kind != KindSSH && p.Kind != KindALSA {
		return &Mixer{p: p}, "", nil
	}
	card := CardOf(p.Device)
	script := fmt.Sprintf(`set -e
F=$(mktemp /tmp/hione-mixer.XXXXXX)
alsactl store -f "$F" %[1]s 2>/dev/null
echo "SAVED=$F"
c() { amixer -q -c %[1]s sset "$@" 2>/dev/null || true; }
c 'Input Source',0 Line
c 'Line Boost' 0
c 'Capture',0 0dB cap
c 'Loopback Mixing' Disabled
c 'Line' 0 mute
amixer -c %[1]s sget 'Capture',0 2>/dev/null | tail -2 || true
amixer -c %[1]s sget 'Input Source',0 2>/dev/null | grep Item0 || true
amixer -c %[1]s contents 2>/dev/null | grep -A2 "name='Line Jack'" | tail -1 || true
`, card)
	out, err := run(p, script)
	m := &Mixer{p: p}
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "SAVED=") {
			m.saved = strings.TrimPrefix(l, "SAVED=")
		}
	}
	if err != nil {
		m.Restore()
		return nil, out, err
	}
	return m, out, nil
}

// Restore puts the mixer back as it was before SetupLineIn.
func (m *Mixer) Restore() error {
	if m == nil || m.saved == "" {
		return nil
	}
	card := CardOf(m.p.Device)
	_, err := run(m.p, fmt.Sprintf(`alsactl restore -f %s %s 2>/dev/null; rm -f %s`, shq(m.saved), card, shq(m.saved)))
	m.saved = ""
	return err
}

// LineJack reports the card's 'Line Jack' sense ("on", "off" or "" when the card has none).
func LineJack(p Parsed) string {
	if p.Kind != KindSSH && p.Kind != KindALSA {
		return ""
	}
	out, _ := run(p, fmt.Sprintf(`amixer -c %s contents 2>/dev/null | grep -A2 "name='Line Jack'" | tail -1`, CardOf(p.Device)))
	out = strings.TrimSpace(out)
	if i := strings.Index(out, "values="); i >= 0 {
		return out[i+7:]
	}
	return ""
}

// Play plays a WAV file through the backend (loopback calibration). For ssh the file is streamed
// over the connection. Blocks until playback ends or ctx is cancelled.
func Play(ctx context.Context, p Parsed, device, path string) error {
	var cmd *exec.Cmd
	switch p.Kind {
	case KindSSH:
		cmd = exec.CommandContext(ctx, "ssh", "-o", "BatchMode=yes", p.Host, "exec aplay -q -D "+shq(device)+" -")
		f, err := openFile(path)
		if err != nil {
			return err
		}
		defer f.Close()
		cmd.Stdin = f
	case KindPW:
		args := []string{}
		if device != "" {
			args = append(args, "--target", device)
		}
		cmd = exec.CommandContext(ctx, "pw-play", append(args, path)...)
	default:
		cmd = exec.CommandContext(ctx, "aplay", "-q", "-D", device, path)
	}
	out, err := cmd.CombinedOutput()
	if err != nil && ctx.Err() == nil {
		return fmt.Errorf("playback: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Wait helper used by callers that poll.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

// Synthetic starts a stream fed by gen instead of a tool: gen fills one block per call. With
// realtime set, blocks are paced to the sample rate (for the live UI); otherwise they come as
// fast as the consumer takes them.
func Synthetic(ctx context.Context, rate, blockFrames int, realtime bool, gen func(blk [][]float64)) *Stream {
	cctx, cancel := context.WithCancel(ctx)
	st := &Stream{C: make(chan [][]float64, 64), cancel: cancel, stderr: &bytes.Buffer{}, done: make(chan struct{})}
	go func() {
		defer close(st.done)
		defer close(st.C)
		per := time.Duration(float64(blockFrames) / float64(rate) * float64(time.Second))
		next := time.Now()
		for {
			blk := [][]float64{make([]float64, blockFrames), make([]float64, blockFrames)}
			gen(blk)
			if realtime {
				next = next.Add(per)
				if !sleepCtx(cctx, time.Until(next)) {
					return
				}
			}
			select {
			case st.C <- blk:
			case <-cctx.Done():
				return
			}
		}
	}()
	return st
}
