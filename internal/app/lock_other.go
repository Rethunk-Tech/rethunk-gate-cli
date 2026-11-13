//go:build !unix

package app

import "os"

// tryLock is a no-op off unix: concurrent runs are not serialised there.
func tryLock(*os.File) (bool, error) { return true, nil }
