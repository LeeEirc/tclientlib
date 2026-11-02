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
				events = append(events, telnetEvent{typ: commandEvent, command: value})
				d.state = decoderData
			default:
				// Unknown Telnet commands are consumed, never exposed as terminal data.
				d.state = decoderData
			}

		case decoderNegotiationOption:
			events = append(events, telnetEvent{
				typ: negotiationEvent, command: d.negotiationCommand, option: value,
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
			if err := d.appendSubByte(value); err != nil {
				return data, events, err
			}

		case decoderSubnegotiationIAC:
			switch value {
			case se:
				events = append(events, telnetEvent{
					typ: subnegotiationEvent,
					option: d.subOption,
					parameters: append([]byte(nil), d.subData...),
				})
				d.subData = d.subData[:0]
				d.state = decoderData
			case iac:
				if err := d.appendSubByte(iac); err != nil {
					return data, events, err
				}
				d.state = decoderSubnegotiationData
			default:
				// Ignore malformed commands inside SB while keeping the stream framed.
				d.state = decoderSubnegotiationData
			}
		}
	}
	return data, events, nil
}

func (d *decoder) appendSubByte(value byte) error {
	if len(d.subData) >= d.maxSubnegotiation {
		d.state = decoderData
		d.subData = d.subData[:0]
		return fmt.Errorf("%w: limit %d", ErrSubnegotiationTooLong, d.maxSubnegotiation)
	}
	d.subData = append(d.subData, value)
	return nil
}
