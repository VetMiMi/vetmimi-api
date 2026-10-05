// Package clock is where code that depends on the current time gets it.
// Production passes time.Now; a test passes a function returning a fixed
// instant, so TOTP steps, session expiry and booking windows are
// deterministic.
package clock

import "time"

// Now returns the current instant.
type Now func() time.Time
