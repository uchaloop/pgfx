// Package page combines independently implemented list and count operations.
//
// Fetch runs them sequentially by default. WithParallel opts into concurrency
// for callbacks backed by resources that support it. The package does not build
// SQL, select pagination or sorting policies, or create transactions. Callers
// own those choices and must keep the list and count filters consistent.
//
// Separate queries may observe different snapshots. For a consistent snapshot,
// use sequential callbacks within a caller-owned REPEATABLE READ transaction.
package page
