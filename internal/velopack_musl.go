//go:build musl || android

package internal

import (
	"fmt"
	"runtime/debug"

	"graphics.gd/classdb/Engine"
)

func (ui *CloudControl) automaticallyUpdate() {
	defer func() {
		setting_up.Store(false)
		if r := recover(); r != nil {
			Engine.Raise(fmt.Errorf("panic during automatic update: %v", r))
			debug.PrintStack()
		}
	}()
	ui.loginUpdate()
}
