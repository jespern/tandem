package tandem

import "errors"

// NextClock is pure: the caller must durably reserve the returned clock and
// write before exposing or exporting the edit. Remote clocks are not inputs.
func NextClock(lastMillis uint64, lastCounter uint16, now uint64) (uint64, uint16, error) {
	if lastMillis >= maxMillis || now >= maxMillis {
		return 0, 0, errors.New("tandem: invalid clock")
	}
	m, c := lastMillis, lastCounter
	if now > m {
		m = now
		c = 0
	} else if c == 65535 {
		m++
		c = 0
	} else {
		c++
	}
	if m >= maxMillis {
		return 0, 0, errors.New("tandem: clock exhausted")
	}
	return m, c, nil
}
