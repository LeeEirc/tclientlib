module telnet_client

go 1.20

require (
	github.com/LeeEirc/tclientlib v0.0.0
	golang.org/x/term v0.10.0
)

require golang.org/x/sys v0.10.0 // indirect

replace github.com/LeeEirc/tclientlib => ../../
