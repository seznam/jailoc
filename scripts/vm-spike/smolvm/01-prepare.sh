#!/bin/bash
set -u
cd "$(dirname "$0")/../../.." || exit 1
mkdir -p scripts/vm-spike/smolvm/logs bin .spike-state/fx
LOG=scripts/vm-spike/smolvm/logs/01.log
(
set -x
date -u
df -h /
go version
go build -o bin/vmprobe ./cmd/jailoc-vmprobe
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o bin/vmprobe-linux ./cmd/jailoc-vmprobe
curl -fLsS --retry 2 https://api.github.com/repos/smol-machines/smolvm/releases/latest -o scripts/vm-spike/smolvm/results/release.json
python3 -c 'import json; r=json.load(open("scripts/vm-spike/smolvm/results/release.json")); print("release",r["tag_name"]); [print(a["name"],a["browser_download_url"]) for a in r["assets"]]'
curl -fLsS https://smolmachines.com/install.sh -o scripts/vm-spike/smolvm/results/install.sh
shasum -a 256 scripts/vm-spike/smolvm/results/install.sh
rg -n 'curl|wget|tar|sha256|shasum|codesign|sudo|rm |mv |cp |VERSION|version|github.com|PREFIX|HOME' scripts/vm-spike/smolvm/results/install.sh
) >"$LOG" 2>&1
status=$?
printf '01 prepare exit=%s\n' "$status"
rg 'release v|release [0-9]|darwin|macos|sha256|install.sh|go version|Avail|error:|Error' "$LOG" | tail -12
printf 'Evidence: %s\n' "$LOG"
exit "$status"
