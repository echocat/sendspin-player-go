#!/usr/bin/env bash
set -euo pipefail

root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

if [[ ! -S /mnt/wslg/PulseServer ]]; then
  echo 'WSLg PulseServer socket is unavailable; this task does not reconfigure the host.' >&2
  exit 2
fi

mkdir -p var/platform-validation/wslg
cat > var/platform-validation/wslg/asound.conf <<'ALSA'
# Private output-only route; no ~/.asoundrc or /etc file is modified.
pcm.!default {
    type asym
    playback.pcm {
        type pulse
    }
    hint {
        show on
        description "WSLg PulseAudio output only"
        ioid "Output"
    }
}
ctl.!default {
    type pulse
}
ALSA

export ALSA_CONFIG_PATH="$root/var/platform-validation/wslg/asound.conf"
export PULSE_SERVER='unix:/mnt/wslg/PulseServer'

# Keep libasound, its Pulse plugin and PortAudio in one Nix dependency closure.
# All routing variables are scoped to this process and its children only.
exec nix-shell -E '
let p = import <nixpkgs> {}; in p.mkShell {
  packages = [ p.gcc p.portaudio p.pkg-config p.alsa-plugins ];
  ALSA_PLUGIN_DIR = "${p.alsa-plugins}/lib/alsa-lib";
}' --run 'CGO_ENABLED=1 mise exec -- go -C _examples/audio-probe run . -backend portaudio -run-silence -output ../../var/platform-validation/wslg/portaudio.json'
