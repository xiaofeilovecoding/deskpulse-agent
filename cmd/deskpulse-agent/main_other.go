//go:build !windows

package main

import "fmt"

func main() {
	fmt.Println("DeskPulse Agent is a Windows GUI application; build it on Windows with -H=windowsgui.")
}
