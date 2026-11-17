// Package page provides pagination and combines list and count results.
// MakePager configures limits; Pager.Bounds calculates bounds, and Pager.Fetch
// runs queries in one read-only REPEATABLE READ transaction.
// The application supplies SQL, matching filters and allowed sort expressions.
// Fetch combines callbacks without calculating bounds or opening a transaction;
// WithParallel requires independent resources and does not ensure a shared snapshot.
package page
