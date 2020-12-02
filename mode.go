package tclientlib

import (
	"log"
)

const (
	DebugMode   = "debug"
	NoPrintMode = "no print"
)
const (
	debugCode = iota
	noPrintCode
)

var currentMode = noPrintCode

func SetMode(value string) {
	switch value {
	case DebugMode, "":
		currentMode = debugCode
	case NoPrintMode:
		currentMode = noPrintCode
	default:
		panic("unknown mode" + value)
	}
	if value == "" {
		value = DebugMode
	}
}

func traceLog(values ...interface{}) {
	if currentMode == debugCode {
		log.Println(values...)
	}
}

func traceLogf(format string, values ...interface{}) {
	if currentMode == debugCode {
		log.Printf(format, values...)
	}
}
