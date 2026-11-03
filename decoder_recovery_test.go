package tclientlib

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
)

func TestOversizedSubnegotiationKeepsFramingEverySplit(t *testing.T) {
	// The escaped IAC followed by SE is payload, not an SB terminator.
	wire := []byte{'a', iac, sb, terminalType, 1, 2, 3, 'X', iac, iac, se, 'Y', iac, se,
		'b', iac, will, echoOption, iac, sb, terminalType, 1, iac, se}
	for split := 0; split <= len(wire); split++ {
		t.Run(fmt.Sprint(split), func(t *testing.T) {
			d := newDecoder(2)
			var output []byte
			var events []telnetEvent
			errorsSeen := 0
			for _, part := range [][]byte{wire[:split], wire[split:]} {
				data, ev, err := d.feed(part)
				output = append(output, data...)
				events = append(events, ev...)
				if err != nil {
					if !errors.Is(err, ErrSubnegotiationTooLong) {
						t.Fatal(err)
					}
					errorsSeen++
				}
				if len(d.subData) > 2 {
					t.Fatal("unbounded subnegotiation")
				}
			}
			if !bytes.Equal(output, []byte("ab")) {
				t.Fatalf("application data = %q", output)
			}
			if errorsSeen != 1 {
				t.Fatalf("errors = %d, want 1", errorsSeen)
			}
			if len(events) != 2 || events[0].typ != negotiationEvent || events[1].typ != subnegotiationEvent || !bytes.Equal(events[1].parameters, []byte{1}) {
				t.Fatalf("events = %#v", events)
			}
		})
	}
}

func TestOversizedEscapedIACEntersDiscardState(t *testing.T) {
	d := newDecoder(1)
	_, _, err := d.feed([]byte{iac, sb, terminalType, 1, iac, iac})
	if !errors.Is(err, ErrSubnegotiationTooLong) {
		t.Fatalf("got %v", err)
	}
	data, ev, err := d.feed([]byte{'X', iac, se, 'z'})
	if err != nil || len(ev) != 0 || string(data) != "z" {
		t.Fatalf("data=%q events=%v err=%v", data, ev, err)
	}
}

func FuzzOversizedSubnegotiationRecovery(f *testing.F) {
	f.Add([]byte{iac, se, iac, iac}, uint8(1))
	f.Add([]byte("ignored-data"), uint8(7))
	f.Fuzz(func(t *testing.T, payload []byte, size uint8) {
		if len(payload) > 4096 {
			payload = payload[:4096]
		}
		wire := []byte{'a', iac, sb, terminalType, 1, 2, 3}
		wire = appendEscapedIAC(wire, payload)
		wire = append(wire, iac, se, 'b')
		d := newDecoder(2)
		var output []byte
		errorsSeen := 0
		for _, chunk := range streamChunks(wire, int(size)+1) {
			data, events, err := d.feed(chunk)
			output = append(output, data...)
			if len(events) != 0 {
				t.Fatalf("oversized SB produced events: %v", events)
			}
			if err != nil {
				if !errors.Is(err, ErrSubnegotiationTooLong) {
					t.Fatal(err)
				}
				errorsSeen++
			}
			if len(d.subData) > 2 {
				t.Fatal("unbounded SB data")
			}
		}
		if string(output) != "ab" || errorsSeen != 1 {
			t.Fatalf("data=%q errors=%d", output, errorsSeen)
		}
	})
}
