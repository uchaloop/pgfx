package shard

// SetAfterRoute sets the hook Read calls right after Route and returns a
// function that restores the previous hook. Tests that use it must not run in
// parallel.
func SetAfterRoute(hook func()) (restore func()) {
	previous := afterRoute
	afterRoute = hook

	return func() { afterRoute = previous }
}
