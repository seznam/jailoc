# smolvm P1 progress

- 01 prepare PASS: macOS arm64, Go 1.27.3; 61 GiB free; vmprobe host/guest built; upstream latest v1.32.3 with checksums asset; installer downloaded for review, not executed because it uses `rm -rf` and optional checksum bypass. Evidence: `logs/01.log`.
- 02 install PASS: v1.32.3 darwin-arm64 checksum OK, extracted under isolated HOME (.spike-state/s, used via /tmp/sv symlink for the 103-byte socket limit). Evidence: `logs/02.log`.
- 03 virtio PASS (P1 steps 2-8): boot, dockerd on disk volumes, build 42s, TestRedisPing PASS, dind PASS, persistence PASS; verdict FAIL (baseline). Evidence: `logs/03-virtio.log`, `results-virtio/`.
- 03 blockpriv / blockpriv6: `--block-private` preview; remaining leaks host own IPv6 addresses + DNS interception artifact. Evidence: `logs/03-blockpriv*.log`, `results-blockpriv*/`.
- Report: `docs/research/candidates/smolvm.md`.
