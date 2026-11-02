package tclientlib

import (
	"bytes"
	"io"
	"net"
	"regexp"
	"testing"
	"time"
)

func TestCompatibleCredentialPromptVariants(t *testing.T) {
	usernamePrompts := []string{
		"Login:",
		"Enter username:",
		"Please enter your username:",
		"Login as:",
		"User Name:",
		"请输入用户名：",
	}
	for _, prompt := range usernamePrompts {
		t.Run("username-"+prompt, func(t *testing.T) {
			config, err := prepareConfig(&ClientConfig{
				User: "admin", LineEnding: LineEndingCRNUL,
			})
			if err != nil {
				t.Fatal(err)
			}
			machine := newLoginMachine(config)
			var output bytes.Buffer
			result, err := machine.handleLine(&output, []byte(prompt), nil)
			if err != nil {
				t.Fatal(err)
			}
			if result != loginCredentialSent || output.String() != "admin\r\x00" {
				t.Fatalf("prompt %q: result=%v output=%q", prompt, result, output.String())
			}
		})
	}

	passwordPrompts := []string{
		"Password:",
		"Please input your password:",
		"Password for admin:",
		"Password (required):",
		"Login password:",
		"请输入密码：",
	}
	for _, prompt := range passwordPrompts {
		t.Run("password-"+prompt, func(t *testing.T) {
			config, err := prepareConfig(&ClientConfig{
				Password: "secret", LineEnding: LineEndingCRNUL,
			})
			if err != nil {
				t.Fatal(err)
			}
			machine := newLoginMachine(config)
			var output bytes.Buffer
			result, err := machine.handleLine(&output, []byte(prompt), nil)
			if err != nil {
				t.Fatal(err)
			}
			if result != loginCredentialSent || output.String() != "secret\r\x00" {
				t.Fatalf("prompt %q: result=%v output=%q", prompt, result, output.String())
			}
		})
	}

	config, err := prepareConfig(&ClientConfig{User: "admin", Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	machine := newLoginMachine(config)
	for _, banner := range []string{"Last login:", "Invalid user:", "Password"} {
		result, err := machine.handleLine(io.Discard, []byte(banner), nil)
		if err != nil {
			t.Fatal(err)
		}
		if result != loginPending {
			t.Fatalf("banner %q was treated as a prompt: %v", banner, result)
		}
	}
}

func TestContinuePromptSendsEnterAndCompletesLogin(t *testing.T) {
	server, conn := net.Pipe()
	serverErr := make(chan error, 1)
	go func() {
		defer server.Close()
		_ = server.SetDeadline(time.Now().Add(time.Second))
		if err := writeFull(server, []byte("Login:")); err != nil {
			serverErr <- err
			return
		}
		if err := expectBytes(server, []byte("admin\r\x00")); err != nil {
			serverErr <- err
			return
		}
		if err := writeFull(server, []byte("Password:")); err != nil {
			serverErr <- err
			return
		}
		if err := expectBytes(server, []byte("secret\r\x00")); err != nil {
			serverErr <- err
			return
		}
		if err := writeFull(server, []byte("\r\nPress RETURN to get started!")); err != nil {
			serverErr <- err
			return
		}
		if err := expectBytes(server, []byte{'\r', 0}); err != nil {
			serverErr <- err
			return
		}
		serverErr <- writeFull(server, []byte("\r\nrouter#"))
	}()

	client, err := NewClientConn(conn, &ClientConfig{
		User: "admin", Password: "secret", Timeout: time.Second,
		LineEnding: LineEndingCRNUL,
	})
	if client != nil {
		_ = client.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestContinuePromptResponseIsBounded(t *testing.T) {
	config, err := prepareConfig(&ClientConfig{
		Password: "secret", LineEnding: LineEndingCRNUL,
	})
	if err != nil {
		t.Fatal(err)
	}
	machine := newLoginMachine(config)
	var output bytes.Buffer
	for index := 0; index < maxContinueResponses; index++ {
		result, err := machine.handleLine(&output, []byte("Press ENTER to continue"), nil)
		if err != nil {
			t.Fatal(err)
		}
		if result != loginContinueSent {
			t.Fatalf("response %d: got %v", index, result)
		}
	}
	result, err := machine.handleLine(&output, []byte("Press ENTER to continue"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result != loginPending {
		t.Fatalf("response after limit: got %v", result)
	}
	if output.Len() != maxContinueResponses*2 {
		t.Fatalf("wrote %d bytes, want %d", output.Len(), maxContinueResponses*2)
	}
}

func TestCompatibleShellPromptsRejectBannerLabels(t *testing.T) {
	config, err := prepareConfig(&ClientConfig{Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	machine := newLoginMachine(config)
	machine.phase = loginAwaitResult

	for _, prompt := range []string{
		"router#",
		"my device #",
		"host%",
		"switch(config)#",
		"[HUAWEI]",
		"[~HUAWEI]",
		"[user@host ~]$",
		"<HUAWEI>",
	} {
		result, err := machine.handleLine(io.Discard, []byte(prompt), nil)
		if err != nil {
			t.Fatal(err)
		}
		if result != loginSuccess {
			t.Fatalf("prompt %q was not recognized: %v", prompt, result)
		}
	}

	for _, banner := range []string{
		"[Warning]",
		"[ERROR]",
		"<Unauthorized>",
		"Login successful >",
		"Press ENTER to continue >",
	} {
		result, err := machine.handleLine(io.Discard, []byte(banner), nil)
		if err != nil {
			t.Fatal(err)
		}
		if result == loginSuccess {
			t.Fatalf("banner %q was treated as a shell prompt", banner)
		}
	}
}

func TestAuthenticationFailureVariants(t *testing.T) {
	config, err := prepareConfig(&ClientConfig{Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	machine := newLoginMachine(config)

	for _, message := range []string{
		"Permission denied, please try again.",
		"Authentication failed. Please retry.",
		"Incorrect username or password.",
		"Invalid login, please try again.",
		"Login failed for user admin.",
		"% Bad passwords",
	} {
		result, err := machine.handleLine(io.Discard, []byte(message), nil)
		if err != nil {
			t.Fatal(err)
		}
		if result != loginFailed {
			t.Fatalf("failure %q was not recognized: %v", message, result)
		}
	}

	for _, banner := range []string{
		"Failed login attempts: 5",
		"Last login failed: 3 times",
		"Authentication failure count: 0",
	} {
		result, err := machine.handleLine(io.Discard, []byte(banner), nil)
		if err != nil {
			t.Fatal(err)
		}
		if result != loginPending {
			t.Fatalf("banner %q was treated as a failure: %v", banner, result)
		}
	}
}

func TestPasswordChangeNarrativesArePrecise(t *testing.T) {
	config, err := prepareConfig(&ClientConfig{Password: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	machine := newLoginMachine(config)

	for _, message := range []string{
		"WARNING: Your password has expired.",
		"The password for admin has expired.",
		"Password expired, please change it now.",
		"You must change your password immediately.",
		"Enter new password:",
		"密码已过期，请修改密码",
	} {
		result, err := machine.handleLine(io.Discard, []byte(message), nil)
		if err != nil {
			t.Fatal(err)
		}
		if result != loginPasswordChangeRequired {
			t.Fatalf("password-change message %q was not recognized: %v", message, result)
		}
	}

	for _, banner := range []string{
		"Password expired policy: disabled",
		"Last password expired date: never",
		"Your password will expire in 7 days",
		"Password expiration check: normal",
	} {
		result, err := machine.handleLine(io.Discard, []byte(banner), nil)
		if err != nil {
			t.Fatal(err)
		}
		if result != loginPending {
			t.Fatalf("informational banner %q was treated as password change: %v", banner, result)
		}
	}
}

func TestCustomContinuePromptReplacesDefault(t *testing.T) {
	config, err := prepareConfig(&ClientConfig{
		Password: "secret", ContinuePrompt: regexp.MustCompile(`GO`),
		LineEnding: LineEndingCRNUL,
	})
	if err != nil {
		t.Fatal(err)
	}
	machine := newLoginMachine(config)
	var output bytes.Buffer

	result, err := machine.handleLine(&output, []byte("Press ENTER to continue"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result != loginPending || output.Len() != 0 {
		t.Fatalf("default continue prompt remained active: result=%v output=%q", result, output.Bytes())
	}

	result, err = machine.handleLine(&output, []byte("GO"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if result != loginContinueSent || !bytes.Equal(output.Bytes(), []byte{'\r', 0}) {
		t.Fatalf("custom continue prompt was not handled: result=%v output=%v", result, output.Bytes())
	}
}
