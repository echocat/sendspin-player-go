#!/usr/bin/env bash
# Deliberately NOT a mise/CI task: explicit local consent and a terminal required.
set -euo pipefail
umask 077
export LC_ALL=C

fail() { printf '%s\n' "$*" >&2; return 1; }
ask() {
    local answer
    printf '%s\n> ' "$1" >&2
    IFS= read -r answer < /dev/tty || return 1
    [[ "$answer" == "$2" ]]
}
mixer() { timeout --kill-after=1s 3s amixer --card S3 -- "$@"; }

# Require both reported channels and their dB readings, not just command success.
# Percentages/raw mixer values do not establish analog voltage.
check_levels() {
    local text=$1 kind=$2 maximum=$3 minimum=${4:--999} line gain count=0
    while IFS= read -r line; do
        if [[ "$line" =~ $kind\ [0-9]+\ \[[0-9]+%\]\ \[(-?[0-9]+(\.[0-9]+)?)dB\]\ \[(on|off)\] ]]; then
            gain=${BASH_REMATCH[1]}
            awk -v gain="$gain" -v maximum="$maximum" -v minimum="$minimum" 'BEGIN { exit !(gain <= maximum && gain >= minimum) }' || return 1
            count=$((count + 1))
        fi
    done <<< "$text"
    [[ "$count" == 2 ]]
}
check_switch() {
    local text=$1 kind=$2 expected=$3 line count=0
    while IFS= read -r line; do
        if [[ "$line" =~ $kind\ [0-9]+\ \[[0-9]+%\]\ \[(-?[0-9]+(\.[0-9]+)?)dB\]\ \[(on|off)\] ]]; then
            [[ "${BASH_REMATCH[3]}" == "$expected" ]] || return 1
            count=$((count + 1))
        fi
    done <<< "$text"
    [[ "$count" == 2 ]]
}

capture_pid=''
playback_pid=''
changed=0
restored=0
finish() {
    local status=$? cleanup_failed=0
    # Signals can arrive inside a function redirected to a log. Do not hide the
    # disconnect prompt in that log: reattach to our already-open terminal first.
    exec 1>&"$terminal_fd" 2>&"$terminal_fd"
    trap - EXIT
    # Further ordinary signals must not enable the saved monitor while wired.
    trap '' INT TERM HUP
    for pid in "$playback_pid" "$capture_pid"; do
        if [[ -n "$pid" ]]; then
            kill -TERM "$pid" 2>/dev/null || true
            wait "$pid" 2>/dev/null || true
        fi
    done
    if [[ "$changed" == 1 ]]; then
        mixer sset Speaker playback mute >> "$work/cleanup.log" 2>&1 || cleanup_failed=1
        mixer sset Mic playback mute >> "$work/cleanup.log" 2>&1 || cleanup_failed=1
        printf '\nAudio processes stopped. Disconnect BOTH cable ends before restoration.\n' >&2
        if ask 'When physically disconnected, type ABGEZOGEN (otherwise settings stay muted).' ABGEZOGEN; then
            if mixer info > "$work/card-before-restore.txt" 2>&1 &&
                grep -q 'USB041e:324d' "$work/card-before-restore.txt" &&
                timeout --kill-after=1s 5s alsactl --no-lock --no-init-fallback --pedantic --file "$work/mixer-before.state" restore S3 >> "$work/cleanup.log" 2>&1 &&
                mixer scontents > "$work/mixer-restored.txt" 2>&1 &&
                cmp -s "$work/mixer-before.txt" "$work/mixer-restored.txt"; then
                restored=1
                printf 'Saved S3 mixer settings restored; simple-mixer readback matches.\n' >&2
            else
                cleanup_failed=1
                # A partial restore may have enabled switches: mute again.
                mixer sset Speaker playback mute >> "$work/cleanup.log" 2>&1 || true
                mixer sset Mic playback mute >> "$work/cleanup.log" 2>&1 || true
                printf 'RESTORE FAILED. Outputs muted again where possible.\n' >&2
            fi
        else
            cleanup_failed=1
            printf 'NOT RESTORED: no disconnect confirmation received.\n' >&2
        fi
        if [[ "$restored" != 1 ]]; then
            printf 'After physically disconnecting, restore manually:\n' >&2
            printf 'alsactl --no-lock --no-init-fallback --pedantic --file "%s" restore S3\n' "$work/mixer-before.state" >&2
        fi
    fi
    if [[ "$cleanup_failed" == 1 && "$status" == 0 ]]; then status=1; fi
    if [[ -n "${work:-}" ]]; then
        printf 'Private artifacts: %s\nDo not upload capture.wav or the entire directory.\n' "$work" >&2
    fi
    exit "$status"
}

main() {
    local capture_db=0 capture_min_db=-999
    if [[ $# -lt 1 || $# -gt 2 || "$1" != --acknowledge-unverified-wiring ]]; then
        fail 'Usage: bash mise-tasks/play3-low-level-loopback.sh --acknowledge-unverified-wiring [--capture-gain-24db]'
        return 2
    fi
    if [[ $# == 2 ]]; then
        [[ "$2" == --capture-gain-24db ]] || { fail 'Only the separately consented --capture-gain-24db mode is supported.'; return 2; }
        capture_db=24
        capture_min_db=24
    fi
    [[ "$(uname -s)" == Linux ]] || { fail 'Linux/WSL only.'; return 2; }
    [[ -t 0 && -t 1 ]] || { fail 'An interactive terminal is required; no piped confirmations.'; return 2; }
    exec {terminal_fd}<> /dev/tty
    for tool in amixer alsactl arecord aplay timeout awk mktemp go grep sleep mkdir cmp; do command -v "$tool" >/dev/null || return 2; done
    local root tool speaker mic
    root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
    printf '%s\n' \
        'PLAY! 3 / S3 only. Close other audio applications using this card.' \
        'This is an UNVERIFIED direct headphone-to-microphone connection.' \
        'Low levels/muting do NOT remove microphone DC bias or prove electrical safety.' \
        'Only an ordinary straight 3.5mm stereo TRS cable (two insulating rings on EACH plug).' \
        'No mono/TRRS cable, splitter, headphones or speakers; no joining L/R outputs.' \
        '3s reference, left only, peak 0.01 (-40dBFS); speaker <= -40dB.' \
        'Five seconds of private microphone recording. No automatic gain increase/retry.' \
        'Ctrl-C stops the run, but stay here to disconnect and restore. Do not close this terminal.'
    printf 'Requested capture gain: %sdB; +24dB requires the explicit extra flag and separate consent.\n' "$capture_db"
    ask 'Cable DISCONNECTED, correct cable, risks accepted? Type VORBEREITEN.' VORBEREITEN || return 2
    mkdir -p "$root/var/platform-validation/play3-low-level"
    work="$(mktemp -d "$root/var/platform-validation/play3-low-level/run-XXXXXXXX")"
    trap finish EXIT
    trap 'exit 130' INT
    trap 'exit 143' TERM HUP
    printf 'capture_gain_db=%s\nspeaker_max_db=-40\nreference_peak=0.01\ncapture_seconds=5\n' "$capture_db" > "$work/run-settings.txt"

    # All compilation/file preparation happens BEFORE changing a mixer/opening audio.
    tool="$work/loopback"
    CGO_ENABLED=0 go -C "$root/_examples/loopback" build -o "$tool" . > "$work/build.log" 2>&1
    "$tool" generate -out "$work/reference.wav" -seconds 3 -amplitude 0.01 > "$work/generate.log" 2>&1
    mixer info > "$work/card.txt"
    grep -q 'USB041e:324d' "$work/card.txt" || { fail 'S3 is not the expected USB041e:324d PLAY! 3; no mixer changes.'; return 1; }
    mixer scontents > "$work/mixer-before.txt"
    timeout --kill-after=1s 5s alsactl --no-lock --file "$work/mixer-before.state" store S3 > "$work/store.log" 2>&1
    [[ -s "$work/mixer-before.state" ]] || { fail 'No usable snapshot; aborting before changes.'; return 1; }

    # A failed first write can still have partially changed hardware: arm cleanup first.
    changed=1
    {
        mixer sset Speaker playback mute
        mixer sset Mic playback mute
        mixer sset Speaker playback -40dB
        mixer sset Mic capture "${capture_db}dB"
        mixer sset Mic capture cap
    } > "$work/setup.log" 2>&1
    speaker="$(mixer sget Speaker)"
    mic="$(mixer sget Mic)"
    printf '%s\n%s\n' "$speaker" "$mic" > "$work/mixer-test.txt"
    if ! check_levels "$speaker" Playback -40 || ! check_switch "$speaker" Playback off; then
        fail 'Speaker level/mute readback failed.'; return 1
    fi
    if ! check_levels "$mic" Capture "$capture_db" "$capture_min_db" || ! check_switch "$mic" Capture on || ! check_switch "$mic" Playback off; then
        fail 'Capture/monitor readback failed.'; return 1
    fi

    printf '%s\n' "$speaker" "$mic"
    ask 'Now connect HEADPHONE-OUT to the SEPARATE MIC-IN with the TRS cable. Type VERBUNDEN to run once.' VERBUNDEN || return 2
    # Recheck after the human wait: another application may have changed the card.
    speaker="$(mixer sget Speaker)"; mic="$(mixer sget Mic)"
    if ! check_levels "$speaker" Playback -40 || ! check_switch "$speaker" Playback off || \
        ! check_levels "$mic" Capture "$capture_db" "$capture_min_db" || ! check_switch "$mic" Capture on || ! check_switch "$mic" Playback off; then
        fail 'Mixer changed while waiting; aborting.'; return 1
    fi

    # Open a new private regular file ourselves. No implicit/default input device.
    timeout --kill-after=1s 7s arecord --device hw:CARD=S3,DEV=0 --file-type wav \
        --format S16_LE --rate 48000 --channels 2 --duration 5 \
        --disable-resample --disable-channels --disable-format --disable-softvol \
        > "$work/capture.wav" 2> "$work/arecord.log" &
    capture_pid=$!
    sleep 0.5
    kill -0 "$capture_pid" 2>/dev/null || { wait "$capture_pid"; fail 'Recorder ended before playback.'; return 1; }
    mixer sset Speaker playback unmute >> "$work/setup.log" 2>&1
    speaker="$(mixer sget Speaker)"; mic="$(mixer sget Mic)"
    if ! check_levels "$speaker" Playback -40 || ! check_switch "$speaker" Playback on || \
        ! check_levels "$mic" Capture "$capture_db" "$capture_min_db" || ! check_switch "$mic" Capture on || ! check_switch "$mic" Playback off; then
        fail 'Playback/monitor recheck failed.'; return 1
    fi
    timeout --kill-after=1s 5s aplay --device hw:CARD=S3,DEV=0 \
        --disable-resample --disable-channels --disable-format --disable-softvol \
        "$work/reference.wav" > "$work/aplay.log" 2>&1 &
    playback_pid=$!
    wait "$playback_pid"
    playback_pid=''
    mixer sset Speaker playback mute >> "$work/setup.log" 2>&1
    wait "$capture_pid"
    capture_pid=''
    if grep -Eiq 'overrun|underrun|xrun' "$work/arecord.log" "$work/aplay.log"; then
        fail 'ALSA reported an xrun; retain diagnostics, do not treat this as a clean run.'
        return 1
    fi
    # Analysis is OFFLINE. Failure/inconclusive does not trigger louder replay.
    local channel result
    for channel in 0 1; do
        result=0
        timeout --kill-after=1s 30s "$tool" analyze -reference "$work/reference.wav" \
            -manifest "$work/reference.wav.json" -capture "$work/capture.wav" \
            -capture-channel "$channel" -source recording -search-ms 2000 \
            -out "$work/analysis-channel$channel.json" > "$work/analysis-channel$channel.log" 2>&1 || result=$?
        printf 'Offline channel %s: exit %s (0=aligned, nonzero=inconclusive/error).\n' "$channel" "$result"
    done
    printf 'No automatic gain increase. File alignment is NOT SDK timing or absolute latency.\n'
}

main "$@"
