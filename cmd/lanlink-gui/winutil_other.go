//go:build !windows

package main

import "errors"

func flashTaskbar(string) {}

func playBeep() {}

func setAutoStart(bool) error {
	return errors.New("开机自启仅支持 Windows")
}
