package tclientlib

const (
	se   byte = 240
	nop  byte = 241
	dm   byte = 242
	brk  byte = 243
	ip   byte = 244
	ao   byte = 245
	ayt  byte = 246
	ec   byte = 247
	el   byte = 248
	ga   byte = 249
	sb   byte = 250
	will byte = 251
	wont byte = 252
	do   byte = 253
	dont byte = 254
	iac  byte = 255
)

const (
	binaryOption     byte = 0
	echoOption       byte = 1
	suppressGAOption byte = 3
	terminalType     byte = 24
	windowSize       byte = 31
	terminalSpeed    byte = 32
	oldEnvironment   byte = 36
	newEnvironment   byte = 39
)

const maxWindowSize = 1<<16 - 1

// Command is a standalone Telnet command sent with Client.SendCommand.
type Command byte

const (
	CommandNoOperation      Command = Command(nop)
	CommandDataMark         Command = Command(dm)
	CommandBreak            Command = Command(brk)
	CommandInterruptProcess Command = Command(ip)
	CommandAbortOutput      Command = Command(ao)
	CommandAreYouThere      Command = Command(ayt)
	CommandEraseCharacter   Command = Command(ec)
	CommandEraseLine        Command = Command(el)
	CommandGoAhead          Command = Command(ga)
)

func validCommand(command Command) bool {
	return byte(command) >= nop && byte(command) <= ga
}
