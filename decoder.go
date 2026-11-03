package tclientlib

import "fmt"

type decoderState uint8

const (
	decoderData decoderState = iota
	decoderIAC
	decoderNegotiationOption
	decoderSubnegotiationOption
	decoderSubnegotiationData
	decoderSubnegotiationIAC
	decoderDiscardSubnegotiation
	decoderDiscardSubnegotiationIAC
)

type eventType uint8

const (
	negotiationEvent eventType = iota
	subnegotiationEvent
	commandEvent
)

type telnetEvent struct {
	typ        eventType
	command    byte
	option     byte
	parameters []byte
	// dataOffset is the number of application bytes decoded before this
	// event in this feed call. It preserves option changes within a read.
	dataOffset int
}

type decoder struct {
	state              decoderState
	negotiationCommand byte
	subOption          byte
	subData            []byte
	maxSubnegotiation  int
}

func newDecoder(maxSubnegotiation int) *decoder {
	return &decoder{maxSubnegotiation: maxSubnegotiation}
}

func (d *decoder) feed(input []byte) ([]byte, []telnetEvent, error) {
	data := make([]byte, 0, len(input))
	events := make([]telnetEvent, 0, 2)
	var decodeErr error

	for _, value := range input {
		switch d.state {
		case decoderData:
			if value == iac {
				d.state = decoderIAC
				continue
			}
			data = append(data, value)

		case decoderIAC:
			switch value {
			case iac:
				data = append(data, iac)
				d.state = decoderData
			case will, wont, do, dont:
				d.negotiationCommand = value
				d.state = decoderNegotiationOption
			case sb:
				d.state = decoderSubnegotiationOption
			case nop, dm, brk, ip, ao, ayt, ec, el, ga:
				events = append(events, telnetEvent{typ: commandEvent, command: value, dataOffset: len(data)})
				d.state = decoderData
			default:
				// Unknown Telnet commands are consumed, never exposed as terminal data.
				d.state = decoderData
			}

		case decoderNegotiationOption:
			events = append(events, telnetEvent{
				typ: negotiationEvent, command: d.negotiationCommand, option: value, dataOffset: len(data),
			})
			d.state = decoderData

		case decoderSubnegotiationOption:
			d.subOption = value
			d.subData = d.subData[:0]
			d.state = decoderSubnegotiationData

		case decoderSubnegotiationData:
			if value == iac {
				d.state = decoderSubnegotiationIAC
				continue
			}
			if err := d.appendSubByte(value); err != nil && decodeErr == nil {
				decodeErr = err
			}

		case decoderSubnegotiationIAC:
			switch value {
			case se:
				events = append(events, telnetEvent{
					typ:        subnegotiationEvent,
					option:     d.subOption,
					parameters: append([]byte(nil), d.subData...),
					dataOffset: len(data),
				})
				d.subData = d.subData[:0]
				d.state = decoderData
			case iac:
				if err := d.appendSubByte(iac); err != nil {
					if decodeErr == nil {
						decodeErr = err
					}
					continue
				}
				d.state = decoderSubnegotiationData
			default:
				// Ignore malformed commands inside SB while keeping the stream framed.
				d.state = decoderSubnegotiationData
			}
		case decoderDiscardSubnegotiation:
			if value == iac {
				d.state = decoderDiscardSubnegotiationIAC
			}
		case decoderDiscardSubnegotiationIAC:
			// IAC IAC is escaped payload even while discarding. Only the
			// actual IAC SE terminator restores the application stream.
			if value == se {
				d.state = decoderData
			} else {
				d.state = decoderDiscardSubnegotiation
			}
		}
	}
	return data, events, decodeErr
}

func (d *decoder) appendSubByte(value byte) error {
	if len(d.subData) >= d.maxSubnegotiation {
		// Report once, but continue scanning this chunk and subsequent reads
		// through IAC SE. Never expose an oversized SB's tail as terminal data.
		d.state = decoderDiscardSubnegotiation
		d.subData = d.subData[:0]
		return fmt.Errorf("%w: limit %d", ErrSubnegotiationTooLong, d.maxSubnegotiation)
	}
	d.subData = append(d.subData, value)
	return nil
}
