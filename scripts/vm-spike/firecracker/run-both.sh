#!/usr/bin/env bash
D=/path/to/jailoc-firecracker/scripts/vm-spike/firecracker
bash "$D/07-08.sh" policy
bash "$D/07-08.sh" baseline
echo ALLDONE > "$D/logs/run-both.done"
