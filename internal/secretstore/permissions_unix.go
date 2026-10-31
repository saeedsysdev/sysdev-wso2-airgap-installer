//go:build !windows

package secretstore

import "os"

func secureDirectory(path string) error        { return os.Chmod(path, 0700) }
func secureFile(path string, f *os.File) error { return f.Chmod(0600) }
func secureExistingFile(path string) error     { return os.Chmod(path, 0600) }
