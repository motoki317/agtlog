// Package leakcheck guards the repository against committed machine-local or
// environment-specific identifiers. The forbidden set is derived at test time
// from the local environment and an optional gitignored denylist, so the test
// contains no private names and reads identically on every machine.
//
// The scan includes untracked files that Git does not ignore, catching leaks
// before they are staged. See AGENTS.md for the repository leakage policy.
package leakcheck
