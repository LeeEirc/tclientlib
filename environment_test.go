package tclientlib

import (
	"bytes"
	"fmt"
	"io"
	"testing"
)

func TestEnvironmentRequestsEverySplit(t *testing.T) {
	tests := []struct {
		name     string
		request  []byte
		response []byte // Includes IS; nil means a malformed SEND is ignored.
	}{
		{"default", nil, []byte("\x00\x00USER\x01alice")},
		{"well known wildcard", []byte{0}, []byte("\x00\x00USER\x01alice")},
		{"user wildcard", []byte{3}, []byte{0}},
		{"all wildcards", []byte{0, 3}, []byte("\x00\x00USER\x01alice")},
		{"explicit USER", []byte("\x00USER"), []byte("\x00\x00USER\x01alice")},
		{"unknown variable", []byte("\x00DISPLAY"), []byte("\x00\x00DISPLAY")},
		{"distinct namespace", []byte("\x03USER"), []byte("\x00\x03USER")},
		{"ordered variables", []byte("\x00DISPLAY\x00USER\x03SECRET"), []byte("\x00\x00DISPLAY\x00USER\x01alice\x03SECRET")},
		{"duplicates and wildcard", []byte("\x00USER\x00\x00USER"), []byte("\x00\x00USER\x01alice\x00USER\x01alice\x00USER\x01alice")},
		{"escaped names", []byte{'\x00', 'a', 2, 0, 2, 1, 2, 2, 2, 3, iac}, []byte{0, 0, 'a', 2, 0, 2, 1, 2, 2, 2, 3, iac}},
		{"VALUE rejected", []byte("\x00USER\x01"), nil},
		{"missing type", []byte("USER"), nil},
		{"incomplete escape", []byte("\x00USER\x02"), nil},
		{"escape before type", []byte{2, 0}, nil},
	}
	for _, option := range []byte{newEnvironment, oldEnvironment} {
		for _, tc := range tests {
			t.Run(fmt.Sprintf("%d/%s", option, tc.name), func(t *testing.T) {
				params := append([]byte{1}, tc.request...)
				wire := append([]byte{iac, do, option}, (optionPacket{command: sb, option: option, parameters: params}).bytes()...)
				want := []byte{iac, will, option}
				if tc.response != nil {
					want = append(want, (optionPacket{command: sb, option: option, parameters: tc.response}).bytes()...)
				}
				for split := 0; split <= len(wire); split++ {
					client, conn := protocolTestClient(t, [][]byte{wire[:split], wire[split:]}, "alice")
					if _, err := io.ReadAll(client); err != nil {
						t.Fatalf("split %d: %v", split, err)
					}
					if !bytes.Equal(conn.writes.Bytes(), want) {
						t.Fatalf("split %d: reply = %v, want %v", split, conn.writes.Bytes(), want)
					}
				}
			})
		}
	}
}

func TestEnvironmentEscapesValueAndIAC(t *testing.T) {
	wire := []byte{iac, do, newEnvironment, iac, sb, newEnvironment, 1, iac, se}
	client, conn := protocolTestClient(t, streamChunks(wire, 1), "a\x00\x01\x02\x03\xffz")
	if _, err := io.ReadAll(client); err != nil {
		t.Fatal(err)
	}
	want := []byte{iac, will, newEnvironment, iac, sb, newEnvironment, 0, 0, 'U', 'S', 'E', 'R', 1,
		'a', 2, 0, 2, 1, 2, 2, 2, 3, iac, iac, 'z', iac, se}
	if !bytes.Equal(conn.writes.Bytes(), want) {
		t.Fatalf("reply = %v, want %v", conn.writes.Bytes(), want)
	}
}

func TestEnvironmentRequiresNegotiationAndConfiguredUser(t *testing.T) {
	send := []byte{iac, sb, newEnvironment, 1, iac, se}
	for _, user := range []string{"", "alice"} {
		wire := append([]byte(nil), send...)
		wire = append(wire, iac, do, newEnvironment, iac, dont, newEnvironment)
		wire = append(wire, send...)
		client, conn := protocolTestClient(t, streamChunks(wire, 1), user)
		if _, err := io.ReadAll(client); err != nil {
			t.Fatal(err)
		}
		want := []byte{iac, wont, newEnvironment}
		if user != "" {
			want = []byte{iac, will, newEnvironment, iac, wont, newEnvironment}
		}
		if !bytes.Equal(conn.writes.Bytes(), want) {
			t.Fatalf("user %q: reply = %v, want %v", user, conn.writes.Bytes(), want)
		}
	}
}

func TestOldEnvironmentReversedCodesPersistPerConnection(t *testing.T) {
	// A BSD-style SEND uses 1 for VAR and 0 for VALUE (RFC 1571).
	wire := []byte{iac, do, oldEnvironment}
	wire = append(wire, (optionPacket{command: sb, option: oldEnvironment, parameters: []byte("\x01\x01DISPLAY\x01USER")}).bytes()...)
	wire = append(wire, (optionPacket{command: sb, option: oldEnvironment, parameters: []byte{1}}).bytes()...)
	wire = append(wire, iac, do, newEnvironment)
	wire = append(wire, (optionPacket{command: sb, option: newEnvironment, parameters: []byte{1}}).bytes()...)
	for split := 0; split <= len(wire); split++ {
		client, conn := protocolTestClient(t, [][]byte{wire[:split], wire[split:]}, "alice")
		if _, err := io.ReadAll(client); err != nil {
			t.Fatal(err)
		}
		want := []byte{iac, will, oldEnvironment}
		want = append(want, (optionPacket{command: sb, option: oldEnvironment, parameters: []byte("\x00\x01DISPLAY\x01USER\x00alice")}).bytes()...)
		want = append(want, (optionPacket{command: sb, option: oldEnvironment, parameters: []byte("\x00\x01USER\x00alice")}).bytes()...)
		want = append(want, iac, will, newEnvironment)
		want = append(want, (optionPacket{command: sb, option: newEnvironment, parameters: []byte("\x00\x00USER\x01alice")}).bytes()...)
		if !bytes.Equal(conn.writes.Bytes(), want) {
			t.Fatalf("split %d: reply = %v, want %v", split, conn.writes.Bytes(), want)
		}
	}
	client, conn := protocolTestClient(t, [][]byte{{iac, do, oldEnvironment, iac, sb, oldEnvironment, 1, iac, se}}, "alice")
	if _, err := io.ReadAll(client); err != nil {
		t.Fatal(err)
	}
	want := []byte{iac, will, oldEnvironment}
	want = append(want, (optionPacket{command: sb, option: oldEnvironment, parameters: []byte("\x00\x00USER\x01alice")}).bytes()...)
	if !bytes.Equal(conn.writes.Bytes(), want) {
		t.Fatalf("new connection retained reversed codes: %v", conn.writes.Bytes())
	}
}

func TestEnvironmentResponseBound(t *testing.T) {
	for _, tc := range []struct {
		limit int
		want  bool
	}{{11, false}, {12, true}} {
		got, _ := environmentResponse(nil, "alice", false, false, tc.limit)
		if (got != nil) != tc.want {
			t.Fatalf("limit %d: response = %v", tc.limit, got)
		}
	}
	got, _ := environmentResponse([]byte{0, 0}, "alice", false, false, 12)
	if got != nil {
		t.Fatalf("wildcard expansion exceeded response bound: %v", got)
	}
}
