package tclientlib

import (
	"bytes"
	"errors"
	"testing"
)

func TestDecoderEverySplitPoint(t *testing.T) {
	wire := []byte{'a', iac, will, echoOption, 'b', iac, iac, 'c', iac, sb, terminalType, 1, iac, se, 'd'}
	for split := 0; split <= len(wire); split++ {
		decoder := newDecoder(defaultMaxSubnegotiation)
		var data []byte
		var events []telnetEvent
		for _, part := range [][]byte{wire[:split], wire[split:]} {
			gotData, gotEvents, err := decoder.feed(part)
			if err != nil {
				t.Fatalf("split %d: %v", split, err)
			}
			data = append(data, gotData...)
			events = append(events, gotEvents...)
		}
		if !bytes.Equal(data, []byte{'a', 'b', iac, 'c', 'd'}) {
			t.Fatalf("split %d: data %v", split, data)
		}
		if len(events) != 2 || events[0].typ != negotiationEvent || events[1].typ != subnegotiationEvent {
			t.Fatalf("split %d: events %#v", split, events)
		}
	}
}

func TestDecoderBoundsSubnegotiation(t *testing.T) {
	decoder := newDecoder(2)
	_, _, err := decoder.feed([]byte{iac, sb, terminalType, 1, 2, 3})
	if !errors.Is(err, ErrSubnegotiationTooLong) {
		t.Fatalf("got %v", err)
	}
}
