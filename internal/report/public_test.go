package report

import "testing"

func TestPublicCapture(t *testing.T) {
	for in, want := range map[string]string{
		"ssh://somehost/hw:0,0":     "hw:0,0 over ssh",
		"ssh://me@somehost/hw:2,0":  "hw:2,0 over ssh",
		"ssh://somehost":            "hw:0,0 over ssh",
		"alsa:hw:1,0":               "alsa:hw:1,0",
		"pw:alsa_input.pci-0000_00": "pw:alsa_input.pci-0000_00",
	} {
		if got := publicCapture(in); got != want {
			t.Errorf("publicCapture(%q) = %q, want %q", in, got, want)
		}
	}
	if got := publicDevice("HiBy M500 (10.0.0.5:5555)"); got != "HiBy M500" {
		t.Errorf("publicDevice = %q", got)
	}
	if got := publicDevice("HiBy M500 / MikuOS"); got != "HiBy M500 / MikuOS" {
		t.Errorf("publicDevice = %q", got)
	}
}
