#!/usr/bin/env bash
# Make a capture card on another machine show up as an ordinary audio input on this one, so the
# browser version of Hione (or any other program) can record from it.
#
#   tools/remote-capture-bridge.sh HOST [DEVICE] [RATE]
#   tools/remote-capture-bridge.sh capture-pc hw:0,0 192000
#
# It does what `hione serve --capture ssh://HOST/DEVICE` does for its own capture: sets the remote
# card's mixer for a clean line capture (line input, no boost, capture at 0 dB, loopback off),
# runs arecord there over ssh, and puts the old mixer back when it stops. The samples go into a
# PipeWire source on this machine named "Remote-line-in". Needs ssh access to
# HOST without a password prompt, arecord and amixer there, and pactl here (PipeWire or
# PulseAudio). Stop it with Ctrl-C.
#
# The source runs at RATE, 32-bit, stereo. A browser records at the sound server's default rate
# (48 kHz on a stock PipeWire), so PipeWire resamples the source to that and the response stops
# near 21 kHz. For the full band, set default.clock.rate = 192000 in a file under
# ~/.config/pipewire/pipewire.conf.d/ and restart PipeWire. The page shows the rate it got.
# (The one-word name is on purpose: PipeWire's PulseAudio layer cuts a description at the first space.)
set -euo pipefail

host="${1:?usage: $0 HOST [DEVICE] [RATE]}"
dev="${2:-hw:0,0}"
rate="${3:-192000}"
card="${dev#hw:}"; card="${card#plughw:}"; card="${card%%,*}"
fifo="${XDG_RUNTIME_DIR:-/tmp}/hione-remote-capture.fifo"
ssh_opts=(-o BatchMode=yes -o ServerAliveInterval=5)

saved=""
module=""
cleanup() {
  trap - EXIT INT TERM
  [ -n "$module" ] && pactl unload-module "$module" 2>/dev/null || true
  rm -f "$fifo"
  if [ -n "$saved" ]; then
    ssh "${ssh_opts[@]}" "$host" "alsactl restore -f '$saved' '$card' 2>/dev/null; rm -f '$saved'" || true
    echo "Mixer on $host restored."
  fi
}
trap cleanup EXIT INT TERM

# The same mixer setup as internal/capture.SetupLineIn. Controls a card lacks are skipped.
saved="$(ssh "${ssh_opts[@]}" "$host" "bash -s" <<EOF | sed -n 's/^SAVED=//p'
set -e
F=\$(mktemp /tmp/hione-mixer.XXXXXX)
alsactl store -f "\$F" $card 2>/dev/null
echo "SAVED=\$F"
c() { amixer -q -c $card sset "\$@" 2>/dev/null || true; }
c 'Input Source',0 Line
c 'Line Boost' 0
c 'Capture',0 0dB cap
c 'Loopback Mixing' Disabled
c 'Line' 0 mute
EOF
)"
echo "Mixer on $host set for line capture (saved to $saved)."

rm -f "$fifo"
mkfifo -m 600 "$fifo"
module="$(pactl load-module module-pipe-source source_name=hione_remote_capture file="$fifo" \
  format=s32le rate="$rate" channels=2 \
  source_properties=device.description=Remote-line-in)"
echo "Input \"Remote-line-in\" is up at $((rate / 1000)) kHz. Ctrl-C to stop."

# arecord keeps running until this script stops; the fifo carries raw S32_LE frames.
ssh "${ssh_opts[@]}" "$host" "exec arecord -q -D '$dev' -f S32_LE -r $rate -c 2 -t raw" > "$fifo"
