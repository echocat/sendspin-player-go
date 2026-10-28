//go:build !windows

package monotime

import "time"

func Now() time.Time { return time.Now() }
func Source() string { return "go-runtime-monotonic" }
