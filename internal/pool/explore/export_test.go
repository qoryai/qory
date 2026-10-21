package explore

// SetDropPlace puts f in place of the place record's hook until the returned func runs.
func SetDropPlace(f func(dir string)) (restore func()) {
	was := dropPlace
	dropPlace = f
	return func() { dropPlace = was }
}
