//go:build !windows

package shell

func shortPath(path string) string { return path }
