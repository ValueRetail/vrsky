package main

import "os"

// Mutation for #446: an unchecked error golangci-lint must report.
func init() { os.Remove("/nonexistent-mutation-446") }
