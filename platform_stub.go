//go:build !windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
)

func replaceFile(src, dst string) error {
	_ = os.Remove(dst)
	return os.Rename(src, dst)
}

func atomicWrite(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return replaceFile(tmp, path)
}

func acquireSingleInstance() (func(), bool, error) { return func() {}, false, nil }
func processAlive(pid int) bool                    { return false }
func prepareHiddenCommand(cmd *exec.Cmd)           {}
func regSetRun(name, command string) error         { return nil }
func regDeleteRun(name string) error               { return nil }

func recoverFreshCodexLaunch(configWriteUnixNano int64) bool { return false }
