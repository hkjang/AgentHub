package store

// How many rows a list may return.
//
// Every clamp in this package used to read "out of range, so use the default",
// which quietly punished the caller who asked for more: `?limit=1000` against a
// ceiling of 200 handed back 50 rows, fewer than `?limit=200` would have, and
// nothing in the answer said the number had been ignored. A script paging by a
// large limit saw a fraction of the table and had every reason to believe it was
// the whole of it.
//
// Asking for too many now means the most that is allowed. The ceiling is still
// the ceiling — it is what keeps one request from reading a year of rows into
// memory — but it is applied as a ceiling rather than as a penalty.
func clampLimit(requested, fallback, ceiling int) int {
	if requested <= 0 {
		return fallback
	}
	if requested > ceiling {
		return ceiling
	}
	return requested
}
