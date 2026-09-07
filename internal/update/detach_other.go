//go:build !windows

package update

import "syscall"

func detachedAttr() *syscall.SysProcAttr { return nil }
