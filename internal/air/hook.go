package air

import (
	"net/url"
	"strings"
)

// hookPath is the path Air serves its hooks under, by which a hook to Air is
// told from one to any other endpoint on the same workstation.
const hookPath = "/air/"

// IsHook reports whether a hook's URL calls Air's hook endpoint.
func IsHook(u *url.URL) bool { return strings.HasPrefix(u.Path, hookPath) }
