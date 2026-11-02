package tclientlib

import "testing"

func TestConfigIsCopied(t *testing.T) {
	config := &ClientConfig{}
	full, err := prepareConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	if config.Timeout != 0 || config.Width != 0 || config.Height != 0 || config.TerminalType != "" {
		t.Fatalf("caller config mutated: %#v", config)
	}
	if full.Width != defaultWidth || full.Height != defaultHeight || full.TerminalType != defaultTerminalType {
		t.Fatalf("defaults not applied: %#v", full)
	}
}
