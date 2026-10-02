//go:build !unix

package main

import "os"

func realTTY() *os.File { return nil }
