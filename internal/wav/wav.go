// Package wav reads and writes RIFF/WAVE files: PCM 16, 24 and 32-bit integer and 32-bit float,
// plain or WAVE_FORMAT_EXTENSIBLE. Samples are float64 in [-1, 1), one slice per channel.
package wav

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
)

// Format is a sample format name as used on the command line.
type Format string

const (
	S16 Format = "S16"
	S24 Format = "S24"
	S32 Format = "S32"
	F32 Format = "F32"
)

// ParseFormat accepts S16, S24, S32, F32 in any case.
func ParseFormat(s string) (Format, error) {
	switch Format(upper(s)) {
	case S16, S24, S32, F32:
		return Format(upper(s)), nil
	}
	return "", fmt.Errorf("unknown sample format %q (use S16, S24, S32 or F32)", s)
}

func upper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}

// Bits returns the container size in bits.
func (f Format) Bits() int {
	switch f {
	case S16:
		return 16
	case S24:
		return 24
	}
	return 32
}

// Float reports whether the format is IEEE float.
func (f Format) Float() bool { return f == F32 }

// Audio is a decoded file.
type Audio struct {
	Rate   int
	Format Format
	Data   [][]float64 // [channel][frame]
}

// Frames returns the number of frames.
func (a *Audio) Frames() int {
	if len(a.Data) == 0 {
		return 0
	}
	return len(a.Data[0])
}

// Write stores a as a WAV file.
func Write(path string, a *Audio) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := bufio.NewWriterSize(f, 1<<20)
	if err := Encode(w, a); err != nil {
		f.Close()
		return err
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Encode writes a WAV stream. Integer formats use TPDF-free rounding with clipping: stimuli are
// generated well below full scale and dither is added by the generator where it matters.
func Encode(w io.Writer, a *Audio) error {
	ch := len(a.Data)
	if ch == 0 {
		return errors.New("wav: no channels")
	}
	n := a.Frames()
	bits := a.Format.Bits()
	bpf := ch * bits / 8
	dataLen := n * bpf
	fmtTag := uint16(1)
	if a.Format.Float() {
		fmtTag = 3
	}
	hdr := make([]byte, 0, 44)
	le := binary.LittleEndian
	hdr = append(hdr, "RIFF"...)
	hdr = le.AppendUint32(hdr, uint32(36+dataLen))
	hdr = append(hdr, "WAVEfmt "...)
	hdr = le.AppendUint32(hdr, 16)
	hdr = le.AppendUint16(hdr, fmtTag)
	hdr = le.AppendUint16(hdr, uint16(ch))
	hdr = le.AppendUint32(hdr, uint32(a.Rate))
	hdr = le.AppendUint32(hdr, uint32(a.Rate*bpf))
	hdr = le.AppendUint16(hdr, uint16(bpf))
	hdr = le.AppendUint16(hdr, uint16(bits))
	hdr = append(hdr, "data"...)
	hdr = le.AppendUint32(hdr, uint32(dataLen))
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	buf := make([]byte, 0, 4096*bpf)
	for i := 0; i < n; i++ {
		for c := 0; c < ch; c++ {
			buf = appendSample(buf, a.Format, a.Data[c][i])
		}
		if len(buf) >= 4000*bpf {
			if _, err := w.Write(buf); err != nil {
				return err
			}
			buf = buf[:0]
		}
	}
	_, err := w.Write(buf)
	return err
}

func clampInt(v float64, max float64) int64 {
	r := math.Round(v * max)
	if r > max-1 {
		r = max - 1
	}
	if r < -max {
		r = -max
	}
	return int64(r)
}

func appendSample(b []byte, f Format, v float64) []byte {
	le := binary.LittleEndian
	switch f {
	case S16:
		return le.AppendUint16(b, uint16(int16(clampInt(v, 32768))))
	case S24:
		x := uint32(int32(clampInt(v, 8388608)))
		return append(b, byte(x), byte(x>>8), byte(x>>16))
	case S32:
		return le.AppendUint32(b, uint32(int32(clampInt(v, 2147483648))))
	default:
		return le.AppendUint32(b, math.Float32bits(float32(v)))
	}
}

// Read loads a WAV file.
func Read(path string) (*Audio, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Decode(bufio.NewReaderSize(f, 1<<20))
}

// Decode reads a WAV stream. It tolerates a data chunk length of 0 or 0xFFFFFFFF, which is what
// arecord and pw-record write when they stream to a pipe: the data then runs to end of stream.
func Decode(r io.Reader) (*Audio, error) {
	var hdr [12]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, fmt.Errorf("wav: %w", err)
	}
	if string(hdr[0:4]) != "RIFF" || string(hdr[8:12]) != "WAVE" {
		return nil, errors.New("wav: not a RIFF/WAVE file")
	}
	le := binary.LittleEndian
	var (
		ch, bits, tag uint16
		rate          uint32
		haveFmt       bool
	)
	for {
		var ck [8]byte
		if _, err := io.ReadFull(r, ck[:]); err != nil {
			return nil, fmt.Errorf("wav: no data chunk: %w", err)
		}
		id := string(ck[0:4])
		size := le.Uint32(ck[4:8])
		switch id {
		case "fmt ":
			b := make([]byte, size)
			if _, err := io.ReadFull(r, b); err != nil {
				return nil, err
			}
			tag = le.Uint16(b[0:2])
			ch = le.Uint16(b[2:4])
			rate = le.Uint32(b[4:8])
			bits = le.Uint16(b[14:16])
			if tag == 0xFFFE && len(b) >= 26 {
				tag = le.Uint16(b[24:26])
			}
			haveFmt = true
			if size%2 == 1 {
				io.CopyN(io.Discard, r, 1)
			}
		case "data":
			if !haveFmt {
				return nil, errors.New("wav: data before fmt")
			}
			var f Format
			switch {
			case tag == 1 && bits == 16:
				f = S16
			case tag == 1 && bits == 24:
				f = S24
			case tag == 1 && bits == 32:
				f = S32
			case tag == 3 && bits == 32:
				f = F32
			default:
				return nil, fmt.Errorf("wav: unsupported format tag %d, %d bits", tag, bits)
			}
			var src io.Reader = r
			if size != 0 && size != 0xFFFFFFFF && size != 0x7FFFF000 {
				src = io.LimitReader(r, int64(size))
			}
			return decodeData(src, f, int(ch), int(rate))
		default:
			skip := int64(size)
			if size%2 == 1 {
				skip++
			}
			if _, err := io.CopyN(io.Discard, r, skip); err != nil {
				return nil, err
			}
		}
	}
}

func decodeData(r io.Reader, f Format, ch, rate int) (*Audio, error) {
	a := &Audio{Rate: rate, Format: f, Data: make([][]float64, ch)}
	bps := f.Bits() / 8
	bpf := bps * ch
	buf := make([]byte, bpf*8192)
	le := binary.LittleEndian
	carry := 0
	for {
		n, err := io.ReadFull(r, buf[carry:])
		n += carry
		frames := n / bpf
		for i := 0; i < frames; i++ {
			for c := 0; c < ch; c++ {
				p := buf[i*bpf+c*bps:]
				var v float64
				switch f {
				case S16:
					v = float64(int16(le.Uint16(p))) / 32768
				case S24:
					x := int32(uint32(p[0])<<8|uint32(p[1])<<16|uint32(p[2])<<24) >> 8
					v = float64(x) / 8388608
				case S32:
					v = float64(int32(le.Uint32(p))) / 2147483648
				case F32:
					v = float64(math.Float32frombits(le.Uint32(p)))
				}
				a.Data[c] = append(a.Data[c], v)
			}
		}
		rem := n - frames*bpf
		copy(buf, buf[frames*bpf:n])
		carry = rem
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			return a, nil
		}
		if err != nil {
			return a, err
		}
	}
}

// DecodeRaw reads headerless interleaved little-endian samples until EOF.
func DecodeRaw(r io.Reader, f Format, ch, rate int) (*Audio, error) {
	return decodeData(r, f, ch, rate)
}
