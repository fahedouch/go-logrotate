//go:build linux
// +build linux

package logrotate

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/fahedouch/go-logrotate"
)

// Example of how to rotate in response to SIGHUP.
func ExampleLogger_Rotate() {
	l := &logrotate.Logger{}
	log.SetOutput(l)
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGHUP)

	go func() {
		for {
			<-c
			l.Rotate()
		}
	}()
}
