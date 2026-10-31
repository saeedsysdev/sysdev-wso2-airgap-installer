//go:build !windows

package auditlog

import "os"

func secureDirectory(path string) error           { return os.Chmod(path, 0700) }
func secureFile(path string, file *os.File) error { return file.Chmod(0600) }
