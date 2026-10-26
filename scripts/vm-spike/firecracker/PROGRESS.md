Step 0 PASS /dev/kvm accessible; elapsed=0s; evidence=scripts/vm-spike/firecracker/logs/00.log
Step 1 PASS install/artifacts exit=0 elapsed=79s; evidence=scripts/vm-spike/firecracker/logs/01.log
Step 2 PASS boot->ssh 32-41s uncontended (nested), 80s+ under host swap; rootfs fixes: default route, mount.nfs, gpgv, resolved drop-in
Step 3 PASS docker-ce 29.9.2 overlayfs; /var/lib/docker + /var/lib/containerd both on /dev/vdb (containerd store fix)
Step 4 BLOCKED environment: npm layer failed (root disk full), BuildKit session healthcheck timeouts in starved guest; logs/04-build-retry.log
Step 5 PARTIAL privileged docker:dind OK; Testcontainers not completed
Step 6 PASS image store survived VMM kill + reboot
Step 7 baseline FAIL 117 (results-baseline); policy (nftables on tap-fc) PASS 306 FAIL 5 probe artifacts (results-policy); invalid-policy run results-policy-run1-invalid
Step 8 recorded: root firecracker, kernel NFS on all L1 interfaces
Report: docs/research/candidates/firecracker.md. FC stopped; jlp-fc-host STOPPED.
