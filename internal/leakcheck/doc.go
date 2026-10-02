// Package leakcheck fails when a committable file contains a local identifier.
// Committable files are tracked files and untracked files that Git does not
// ignore, so the scan finds a leak before it is staged.
//
// The test builds the forbidden set at run time, so its source names nothing
// private. The set has kubeconfig context, cluster, user, and namespace names,
// plus the AWS account IDs and ARN tails in those names. It also has the tokens
// in the gitignored .leakcheck file and in AGTLOG_LEAKCHECK_EXTRA. Machine-local
// paths, hostnames, and project names reach the guard only through those two
// lists. AGENTS.md states the policy.
package leakcheck
