// Command mikumeasure measures audio devices: frequency response, distortion, noise, crosstalk
// and impulse response, through a capture card, with an automated DAC matrix for the HiBy M500
// on MikuOS. Run "mikumeasure help" for the commands.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/sworrl/mikumeasure/internal/adb"
	"github.com/sworrl/mikumeasure/internal/analysis"
	"github.com/sworrl/mikumeasure/internal/capture"
	"github.com/sworrl/mikumeasure/internal/engine"
	"github.com/sworrl/mikumeasure/internal/report"
	"github.com/sworrl/mikumeasure/internal/server"
	"github.com/sworrl/mikumeasure/internal/stim"
	"github.com/sworrl/mikumeasure/internal/wav"
)

const usage = `mikumeasure: audio measurements through a capture card.

Commands:
  devices     list capture and playback devices (local, ssh hosts, adb)
  serve       start the live web UI and JSON API (default 127.0.0.1:8484)
  level       level check: play a tone at the test level and read it at the input
  measure     one measurement of the device as it is set now
  matrix      every DAC mode on the M500 (filters x gains x rates, then high power and DRE)
  calibrate   capture chain: "calibrate loopback" (PC out into PC in) or "calibrate level --vrms V"
  report      rebuild the report of a run directory
  generate    write the stimulus WAVs without playing anything

Common flags (any command):
  --capture URI     ssh://host/hw:0,0, alsa:hw:1,0, pw:<node>, sim  (default ssh://host/hw:0,0)
  --rate HZ         capture rate (default 192000)
  --format F        capture format S16, S32 or F32 (default S32)
  --device D        m500 (adb), manual, or sim (default m500)
  --adb SERIAL      adb serial or t:<transport id> (default: first MikuOS device)
  --level DB        test level in dBFS (default -12, no louder than -6)
  --out DIR         output directory (default ./mikumeasure-out)
  --no-mixer        leave the capture card's mixer as it is

Run "mikumeasure <command> -h" for the command's own flags.
`

type common struct {
	capture, format, device, adbSerial, out, cal string
	rate                                      int
	level                                     float64
	noMixer                                   bool
}

func addCommon(fs *flag.FlagSet) *common {
	c := &common{}
	fs.StringVar(&c.capture, "capture", "ssh://host/hw:0,0", "capture URI")
	fs.IntVar(&c.rate, "rate", 192000, "capture rate")
	fs.StringVar(&c.format, "format", "S32", "capture format")
	fs.StringVar(&c.device, "device", "m500", "device under test: m500, manual or sim")
	fs.StringVar(&c.adbSerial, "adb", "", "adb serial or t:<id>")
	fs.Float64Var(&c.level, "level", -12, "test level, dBFS")
	fs.StringVar(&c.out, "out", "mikumeasure-out", "output directory")
	fs.BoolVar(&c.noMixer, "no-mixer", false, "leave the mixer alone")
	fs.StringVar(&c.cal, "cal", "", "capture calibration JSON (default <out>/calibration.json when present)")
	return c
}

// calFile is what calibrate writes.
type calFile struct {
	DBVAtFS  *float64            `json:"dbv_at_fs,omitempty"`
	Loopback *analysis.ChainCal  `json:"loopback,omitempty"`
	Notes    string              `json:"notes,omitempty"`
}

func (c *common) calPath() string {
	if c.cal != "" {
		return c.cal
	}
	return filepath.Join(c.out, "calibration.json")
}

func (c *common) loadCal() calFile {
	var f calFile
	b, err := os.ReadFile(c.calPath())
	if err == nil {
		json.Unmarshal(b, &f)
	}
	return f
}

func (c *common) engine(console bool) (*engine.Engine, error) {
	f, err := wav.ParseFormat(c.format)
	if err != nil {
		return nil, err
	}
	cal := c.loadCal()
	dbv := math.NaN()
	if cal.DBVAtFS != nil {
		dbv = *cal.DBVAtFS
	}
	if c.device == "sim" && c.capture == "ssh://host/hw:0,0" {
		c.capture = "sim"
	}
	return engine.New(engine.Config{
		Capture: c.capture, CapRate: c.rate, CapFormat: f, OutDir: c.out, Device: c.device,
		ADBSerial: c.adbSerial, Level: c.level, NoMixer: c.noMixer, DBVAtFS: dbv, Loopback: cal.Loopback,
		Console: console, Prompt: promptEnter,
	})
}

func promptEnter(msg string) error {
	fmt.Printf("\n>>> %s\n    Press Enter to continue.", msg)
	_, err := bufio.NewReader(os.Stdin).ReadString('\n')
	return err
}

func main() {
	if len(os.Args) < 2 {
		fmt.Print(usage)
		os.Exit(2)
	}
	engine.ReportHook = report.Write
	engine.StepCSVHook = report.StepCSV
	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "devices":
		err = cmdDevices(args)
	case "serve":
		err = cmdServe(args)
	case "level":
		err = cmdRun(args, "level")
	case "measure":
		err = cmdRun(args, "measure")
	case "matrix":
		err = cmdRun(args, "matrix")
	case "calibrate":
		err = cmdCalibrate(args)
	case "report":
		err = cmdReport(args)
	case "generate":
		err = cmdGenerate(args)
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func cmdDevices(args []string) error {
	fs := flag.NewFlagSet("devices", flag.ExitOnError)
	ssh := fs.String("ssh", "", "comma-separated ssh hosts to list too (empty for none)")
	fs.Parse(args)
	var hosts []string
	for _, h := range strings.Split(*ssh, ",") {
		if h = strings.TrimSpace(h); h != "" {
			hosts = append(hosts, h)
		}
	}
	ds, notes := capture.List(hosts)
	fmt.Println("Capture:")
	for _, d := range ds {
		if d.Direction == "capture" {
			fmt.Printf("  %-34s %s\n", d.URI, d.Description)
		}
	}
	fmt.Println("Playback:")
	for _, d := range ds {
		if d.Direction == "playback" {
			fmt.Printf("  %-34s %s\n", d.URI, d.Description)
		}
	}
	for _, n := range notes {
		fmt.Println("note:", n)
	}
	if d, err := adb.Find(""); err == nil {
		st, _ := d.DACState()
		fmt.Printf("adb: %s, DAC %s, wired output %q, volume %s\n", d.Label, st, engine.WiredOutput(d), d.Volume())
	} else {
		fmt.Println("adb:", err)
	}
	return nil
}

func withSignals(e *engine.Engine) (context.Context, func()) {
	ctx, cancel := context.WithCancel(context.Background())
	sig := make(chan os.Signal, 2)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		fmt.Println("\nStopping. Restoring the DAC and the mixer...")
		e.StopRun()
		for i := 0; i < 100 && e.Busy(); i++ {
			time.Sleep(100 * time.Millisecond)
		}
		e.Close()
		os.Exit(130)
	}()
	return ctx, func() { cancel(); signal.Stop(sig) }
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	c := addCommon(fs)
	addr := fs.String("addr", "127.0.0.1:8484", "listen address")
	fs.Parse(args)
	e, err := c.engine(true)
	if err != nil {
		return err
	}
	ctx, done := withSignals(e)
	defer done()
	if err := e.Start(ctx); err != nil {
		return err
	}
	defer e.Close()
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return err
	}
	fmt.Printf("mikumeasure UI: http://%s/\n", ln.Addr())
	srv := &server.Server{E: e}
	return http.Serve(ln, srv.Handler())
}

func parseInts(s string) ([]int, error) {
	var out []int
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		v, err := strconv.Atoi(p)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func cmdRun(args []string, kind string) error {
	fs := flag.NewFlagSet(kind, flag.ExitOnError)
	c := addCommon(fs)
	plan := fs.String("plan", "quick", "quick or full (adds stepped sines and IMD)")
	rates := fs.String("rates", "", "playback rates, comma-separated (matrix default 44100,96000; else 44100)")
	filters := fs.String("filters", "", "matrix filters (default all five): "+strings.Join(adb.Filters, ","))
	gains := fs.String("gains", "low,high", "matrix gains")
	powerDRE := fs.Bool("power-dre", true, "matrix: add high power x DRE runs at your filter")
	skipLevel := fs.Bool("skip-level", false, "skip the level check (not recommended)")
	label := fs.String("label", "", "label for a single measurement")
	jack := fs.String("jack", "3.5mm", "which output jack is connected (for the report)")
	fs.Parse(args)
	e, err := c.engine(true)
	if err != nil {
		return err
	}
	ctx, done := withSignals(e)
	defer done()
	if err := e.Start(ctx); err != nil {
		return err
	}
	defer e.Close()
	spec := engine.RunSpec{Kind: kind, Plan: *plan, Gains: splitList(*gains), Filters: splitList(*filters),
		PowerDRE: *powerDRE, SkipLevel: *skipLevel, Label: *label, Jack: *jack}
	if *rates != "" {
		if spec.Rates, err = parseInts(*rates); err != nil {
			return err
		}
	}
	r, err := e.RunSync(spec)
	if r != nil {
		fmt.Printf("\nRun %s: %s\n", r.ID, r.Status)
		fmt.Println("Results:", r.Dir)
	}
	return err
}

func cmdCalibrate(args []string) error {
	if len(args) == 0 || (args[0] != "loopback" && args[0] != "level") {
		return errors.New(`use "calibrate loopback" or "calibrate level --vrms V"`)
	}
	mode := args[0]
	fs := flag.NewFlagSet("calibrate", flag.ExitOnError)
	c := addCommon(fs)
	playDev := fs.String("play", "", "loopback: playback device (default: the capture card's hw device)")
	vrms := fs.Float64("vrms", 0, "level: the RMS voltage of the 1 kHz tone at the input, measured with a meter")
	fs.Parse(args[1:])
	c.device = "manual"
	e, err := c.engine(true)
	if err != nil {
		return err
	}
	ctx, done := withSignals(e)
	defer done()
	if err := e.Start(ctx); err != nil {
		return err
	}
	defer e.Close()
	cal := c.loadCal()
	switch mode {
	case "loopback":
		p, _ := capture.Parse(c.capture)
		dev := *playDev
		if dev == "" {
			dev = p.Device
		}
		e.Logf("Loopback: playing on %s %s, recording on %s.", p.Host, dev, c.capture)
		cc, res, err := e.Calibrate(ctx, func(path string) error { return capture.Play(ctx, p, dev, path) })
		if err != nil {
			return err
		}
		cal.Loopback = cc
		lo, hi := cc.FR.Range(20, 20000)
		fmt.Printf("Capture chain: FR %+.2f/%+.2f dB (20 Hz-20 kHz), THD+N %.1f dB, noise %.1f dBFS (%.1f A).\n", lo, hi, cc.THDN, cc.NoiseDBFS, cc.NoiseA)
		_ = res
	case "level":
		if *vrms <= 0 {
			return errors.New("give --vrms: the voltage of the 1 kHz tone feeding the input")
		}
		e.Logf("Feed a steady 1 kHz sine of %.4f Vrms into the input now. Measuring for 3 s.", *vrms)
		time.Sleep(1 * time.Second)
		snap := e.Live().Snapshot()
		time.Sleep(3 * time.Second)
		snap = e.Live().Snapshot()
		lv := math.Max(snap.RMSDBFS[0], snap.RMSDBFS[1])
		if lv < -60 {
			return fmt.Errorf("input reads %.1f dBFS, too low to calibrate", lv)
		}
		v := 20*math.Log10(*vrms) - lv
		cal.DBVAtFS = &v
		fmt.Printf("Input reads %.2f dBFS for %.4f Vrms: 0 dBFS = %.2f dBV (%.3f Vrms).\n", lv, *vrms, v, math.Pow(10, v/20))
	}
	cal.Notes = fmt.Sprintf("capture %s at %d Hz %s, mixer set by mikumeasure (line input, no boost, capture 0 dB). A different mixer setting invalidates the level calibration.", c.capture, c.rate, c.format)
	b, _ := json.MarshalIndent(cal, "", " ")
	os.MkdirAll(filepath.Dir(c.calPath()), 0o755)
	if err := os.WriteFile(c.calPath(), b, 0o644); err != nil {
		return err
	}
	fmt.Println("Saved", c.calPath())
	return nil
}

func cmdReport(args []string) error {
	fs := flag.NewFlagSet("report", flag.ExitOnError)
	fs.Parse(args)
	if fs.NArg() != 1 {
		return errors.New("give a run directory (mikumeasure-out/runs/<id>)")
	}
	b, err := os.ReadFile(filepath.Join(fs.Arg(0), "run.json"))
	if err != nil {
		return err
	}
	var r engine.Run
	if err := json.Unmarshal(b, &r); err != nil {
		return err
	}
	r.Dir = fs.Arg(0)
	if err := report.Write(&r); err != nil {
		return err
	}
	fmt.Println("Report written to", r.Dir)
	return nil
}

func cmdGenerate(args []string) error {
	fs := flag.NewFlagSet("generate", flag.ExitOnError)
	plan := fs.String("plan", "quick", "quick, full or level")
	rates := fs.String("rates", "44100,48000,96000,192000", "rates")
	level := fs.Float64("level", -12, "test level, dBFS")
	out := fs.String("out", "mikumeasure-out/stimuli", "directory")
	fs.Parse(args)
	rs, err := parseInts(*rates)
	if err != nil {
		return err
	}
	os.MkdirAll(*out, 0o755)
	for _, r := range rs {
		p, err := stim.Build(stim.Options{Name: *plan, Level: *level, PlayRate: float64(r)})
		if err != nil {
			return err
		}
		path := filepath.Join(*out, fmt.Sprintf("mikumeasure-%s-%.0fdB-%d.wav", p.Name, -p.Level, r))
		if err := wav.Write(path, &wav.Audio{Rate: r, Format: wav.S24, Data: p.Render(float64(r), 24)}); err != nil {
			return err
		}
		pj, _ := json.MarshalIndent(p, "", " ")
		os.WriteFile(strings.TrimSuffix(path, ".wav")+".json", pj, 0o644)
		fmt.Printf("%s (%.1f s)\n", path, p.Total)
	}
	return nil
}
