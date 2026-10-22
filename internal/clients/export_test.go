package clients

// SetTestHook installs Take's step hook for one test; the returned func
// removes it.
func SetTestHook(h func(step, tmp string)) func() {
	testHook = h
	return func() { testHook = nil }
}
