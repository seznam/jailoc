#!/usr/bin/env bash
# Multi-workspace spike: two go-microvm workspaces, each behind its own
# supervised jailoc-netbroker, probe each other and the host, then exercise
# supervision (broker crash, VM crash, invalid policy).
#
# Required: RUNNER (go-microvm-runner built with WithDisks support, see
# stacklok/go-microvm#128). Optional: CACHE (shared OCI image cache dir),
# STATE (default $HOME/.cache/jailoc-vmspike/multi).
set -uo pipefail
cd "$(dirname "$0")/../../.."
ROOT=$PWD
RUNNER=${RUNNER:?set RUNNER to a go-microvm-runner with virtio-blk disk support}
CACHE=${CACHE:-}
S=${STATE:-$HOME/.cache/jailoc-vmspike/multi}
D=scripts/vm-spike/multi
mkdir -p "$S/bin"
exec > >(tee "$S/multi.log") 2>&1
set -x
date -u

# Binaries.
go build -o "$S/bin/vmprobe" ./cmd/jailoc-vmprobe || exit 1
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -o "$S/bin/vmprobe-linux" ./cmd/jailoc-vmprobe || exit 1
go build -o "$S/bin/jailoc-netbroker" ./cmd/jailoc-netbroker || exit 1
(cd $D/launcher && CGO_ENABLED=1 go build -o "$S/bin/launcher" .) || exit 1

HOSTIP=$(ipconfig getifaddr "$(route -n get default | awk '/interface:/{print $2}')")
declare -A NET=([a]=192.168.127 [b]=192.168.128) FWD=([a]=47853 [b]=47863) PEER=([a]=b [b]=a)
echo '{}' > "$S/policy.json"
printf '{"exceptions":[' > "$S/policy-bad.json"

for w in a b; do
  W=$S/$w
  [ -d "$W" ] && rm -r "$W"
  mkdir -p "$W/shares/jlp/out" "$W/state"
  echo $w > "$W/shares/jlp/name"
  o=${PEER[$w]}
  "$S/bin/vmprobe" plan -candidate go-microvm+netbroker-$w -dir "$W/shares/fx" -guest-prefix /mnt/fx \
    -port 47851 -alt-port 47852 -ipv6 disabled \
    -peer ${NET[$o]}.2 -peer "$HOSTIP:${FWD[$o]}" \
    -export "$W/shares/jlp:/mnt/jlp:rw" -out "$W/plan.json" || exit 1
  cp "$W/plan.json" "$S/bin/vmprobe-linux" "$W/shares/jlp/"
done

/usr/bin/python3 cmd/jailoc-vmprobe/sentinel.py host "$S/hits-host.jsonl" 47851 47852 > "$S/sentinel.out" 2>&1 &
SPID=$!
sleep 1
for w in a b; do "$S/bin/vmprobe" selftest -plan "$S/$w/plan.json" -out "$S/$w/r-selftest.json" | tail -1; done

launch() { # name policy
  local w=$1 W=$S/$1
  "$S/bin/launcher" -name "$w" -image docker:29-dind -mem 1536 -cpus 2 -state "$W/state" ${CACHE:+-cache "$CACHE"} \
    -shares "$W/shares" -init "$D/multi-init.sh" -runner "$RUNNER" -broker "$S/bin/jailoc-netbroker" \
    -policy "$2" -subnet "${NET[$w]}.0/24" -forward "127.0.0.1:${FWD[$w]}=4096" > "$W/launcher.log" 2>&1 &
}
waitfor() { # file timeout
  for i in $(seq 1 "$2"); do [ -e "$1" ] && return 0; sleep 1; done
  echo "timeout waiting for $1"; return 1
}

# 1. Two workspaces at once.
t0=$(date +%s)
launch a "$S/policy.json"; LA=$!
launch b "$S/policy.json"; LB=$!
waitfor "$S/a/shares/jlp/out/READY" 300 && waitfor "$S/b/shares/jlp/out/READY" 300
echo "both ready after $(( $(date +%s) - t0 ))s"
cat "$S/a/launcher.log" "$S/b/launcher.log"
touch "$S/a/shares/jlp/go" "$S/b/shares/jlp/go"
for w in a b; do waitfor "$S/$w/shares/jlp/out/HTTPD" 900; done
for w in a b; do "$S/bin/vmprobe" ingress -plan "$S/$w/plan.json" -port ${FWD[$w]} -out "$S/$w/r-ingress.json" | tail -1; done
for w in a b; do waitfor "$S/$w/shares/jlp/out/DONE" 120; done
for w in a b; do
  echo "== $w"; pid=$(awk '/broker-ready/{sub("pid=","",$3); print $3}' "$S/$w/launcher.log")
  lsof -nP -a -p "$pid" -iTCP -sTCP:LISTEN; lsof -nP -a -p "$pid" -U | grep -c sock
done

# 2. Broker crash in b: the supervisor must stop b's VM.
BPB=$(awk '/broker-ready/{sub("pid=","",$3); print $3}' "$S/b/launcher.log")
VMB=$(awk '/vm-started/{sub("id=","",$3); print $3}' "$S/b/launcher.log")
kill -9 "$BPB"; t1=$(date +%s)
wait $LB; echo "b launcher exit=$? after $(( $(date +%s) - t1 ))s"
tail -3 "$S/b/launcher.log"
kill -0 "$VMB" 2>/dev/null && echo "b VM STILL RUNNING" || echo "b VM gone"

# 3. VM crash in a: the supervisor must stop a's broker.
BPA=$(awk '/broker-ready/{sub("pid=","",$3); print $3}' "$S/a/launcher.log")
VMA=$(awk '/vm-started/{sub("id=","",$3); print $3}' "$S/a/launcher.log")
kill -9 "$VMA"; t1=$(date +%s)
wait $LA; echo "a launcher exit=$? after $(( $(date +%s) - t1 ))s"
tail -3 "$S/a/launcher.log"
kill -0 "$BPA" 2>/dev/null && echo "a broker STILL RUNNING" || echo "a broker gone"

# 4. Invalid policy: no VM may start.
mkdir -p "$S/bad/shares/jlp"; cp -r "$S/a/shares/fx" "$S/bad/shares/"; echo bad > "$S/bad/shares/jlp/name"
"$S/bin/launcher" -name bad -state "$S/bad/state" -shares "$S/bad/shares" -init "$D/multi-init.sh" -runner "$RUNNER" \
  -broker "$S/bin/jailoc-netbroker" -policy "$S/policy-bad.json" > "$S/bad/launcher.log" 2>&1
echo "bad launcher exit=$?"; cat "$S/bad/launcher.log"
[ -d "$S/bad/state/vm" ] && echo "bad VM data dir created" || echo "no VM started"

kill $SPID
pgrep -fl 'jailoc-netbroker|go-microvm-runner' || echo "no broker or runner left"

# Verdicts: host sentinel plus the peer's guest sentinel. A workspace's own guest
# sentinel runs inside its domain, so its hits are not evidence against it.
declare -A PEER=([a]=b [b]=a)
for w in a b; do
  "$S/bin/vmprobe" verdict -plan "$S/$w/plan.json" -hits "$S/hits-host.jsonl" \
    -hits "$S/${PEER[$w]}/shares/jlp/out/hits-guest.jsonl" \
    -out "$S/$w/verdict.md" "$S/$w"/r-*.json "$S/$w"/shares/jlp/out/r-*.json
  echo "verdict $w rc=$?"; sed -n 7p "$S/$w/verdict.md"
done
echo MULTI-DONE
