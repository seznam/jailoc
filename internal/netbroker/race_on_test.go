//go:build race

package netbroker

// raceEnabled shrinks stress tests: the race detector slows gVisor down ~100x.
const raceEnabled = true
