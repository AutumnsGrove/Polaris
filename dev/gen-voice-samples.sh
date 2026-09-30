#!/usr/bin/env bash
#
# Regenerates the pre-rendered Settings voice-picker previews in
# web/static/voice-samples/ (issue #114). Each clip is the same short line
# spoken by one voice in voice/voices.go, so the picker can play a preview
# instantly with no TTS call (and no cost) at click time. Re-run after
# changing SAMPLE_TEXT or the roster; commit the resulting mp3s.
#
# Reads openrouter.api_key from ./config.yaml (never printed). Uses the same
# Together pin as production — see config.yaml's tts_provider.
set -euo pipefail
cd "$(dirname "$0")/.."

SAMPLE_TEXT="Hey, I'm Polaris. Ask me anything, I'll dig up the answer and show my sources."
VOICES=(af_sky am_onyx bf_lily bm_daniel)
OUT=web/static/voice-samples

KEY=$(awk '/^openrouter:/{f=1;next} f&&/api_key:/{gsub(/["\047 ]/,"",$2);print $2;exit}' config.yaml)
[ -n "$KEY" ] || { echo "no openrouter.api_key in config.yaml" >&2; exit 1; }

mkdir -p "$OUT"
for v in "${VOICES[@]}"; do
  echo "generating $v"
  curl -sf https://openrouter.ai/api/v1/audio/speech \
    -H "Authorization: Bearer $KEY" -H "Content-Type: application/json" \
    -d "{\"model\":\"hexgrad/kokoro-82m\",\"input\":\"$SAMPLE_TEXT\",\"voice\":\"$v\",\"response_format\":\"mp3\",\"provider\":{\"only\":[\"Together\"]}}" \
    -o "$OUT/$v.mp3"
done
ls -l "$OUT"
