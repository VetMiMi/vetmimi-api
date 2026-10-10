// Package clock is how code gets the current time: production passes
// time.Now, tests pass a fixed instant.
package clock

import "time"

type Now func() time.Time
