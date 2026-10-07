package managementapi

import "os"

// osGetenv is a seam for tests; production reads the real environment.
var osGetenv = os.Getenv
