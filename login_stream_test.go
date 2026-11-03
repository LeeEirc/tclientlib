package tclientlib

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// streamTestConn makes Read boundaries deterministic without treating them as
// message boundaries. Separate net.Pipe tests exercise interactive blocking.
type streamTestConn struct {
	chunks    [][]byte
	writes    bytes.Buffer
	closed    atomic.Bool
	lastError error
}

func (c *streamTestConn) Read(p []byte) (int, error) {
	if c.closed.Load() {
		return 0, net.ErrClosed
	}
	for len(c.chunks) > 0 && len(c.chunks[0]) == 0 {
		c.chunks = c.chunks[1:]
	}
	if len(c.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, c.chunks[0])
	c.chunks[0] = c.chunks[0][n:]
	if len(c.chunks[0]) == 0 {
		c.chunks = c.chunks[1:]
		if len(c.chunks) == 0 && c.lastError != nil {
			return n, c.lastError
		}
	}
	return n, nil
}

func (c *streamTestConn) Write(p []byte) (int, error) {
	if c.closed.Load() {
		return 0, net.ErrClosed
	}
	return c.writes.Write(p)
}
func (c *streamTestConn) Close() error                   { c.closed.Store(true); return nil }
func (c *streamTestConn) LocalAddr() net.Addr            { return streamTestAddr("local") }
func (c *streamTestConn) RemoteAddr() net.Addr           { return streamTestAddr("remote") }
func (c *streamTestConn) SetDeadline(time.Time) error    { return nil }
func (c *streamTestConn) SetReadDeadline(time.Time) error { return nil }
func (c *streamTestConn) SetWriteDeadline(time.Time) error { return nil }

type streamTestAddr string

func (a streamTestAddr) Network() string { return "test" }
func (a streamTestAddr) String() string  { return string(a) }

func streamChunks(data []byte, size int) [][]byte {
	var chunks [][]byte
	for len(data) > 0 {
		n := size
		if n > len(data) {
			n = len(data)
		}
		chunks = append(chunks, data[:n])
		data = data[n:]
	}
	return chunks
}

func checkBannerStream(t *testing.T, parts [][]byte) {
	t.Helper()
	conn := &streamTestConn{chunks: parts, lastError: io.EOF}
	client, err := NewClientConn(conn, &ClientConfig{User: "admin", Password: "secret", Timeout: 5 * time.Second})
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer client.Close()
	got, err := io.ReadAll(client)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "\r\nTAIL" {
		t.Fatalf("unconsumed data = %q, want CRLF+TAIL", got)
	}
	if conn.writes.String() != "admin\r\nsecret\r\n" {
		t.Fatalf("writes = %q", conn.writes.String())
	}
}

func TestLoginStreamDoesNotClassifyMessagePrefixes(t *testing.T) {
	for _, banner := range []string{
		"Authentication failed count: 0", "Authentication failure count: 0",
		"Login failed attempts: 5", "Password expired policy: disabled",
		"New password policy: 90 days", "密码已过期检查：正常",
		"登录成功 >", "密码错误>", "系统警告 >", "[Warning]#",
	} {
		t.Run(banner, func(t *testing.T) {
			data := []byte("Login:\r\nPassword:\r\n" + banner + "\r\nrouter#\r\nTAIL")
			for _, size := range []int{1, 2, 7, 1024, 4096} {
				t.Run(fmt.Sprint(size), func(t *testing.T) {
					checkBannerStream(t, streamChunks(data, size))
				})
			}
		})
	}
}

func TestLoginStreamEverySplitPreservesTail(t *testing.T) {
	data := []byte("Login:\r\nPassword:\r\nPassword expired policy: disabled\r\nrouter#\r\nTAIL")
	for split := 0; split <= len(data); split++ {
		t.Run(fmt.Sprint(split), func(t *testing.T) {
			checkBannerStream(t, [][]byte{data[:split], data[split:]})
		})
	}
}

func TestLoginStreamFinalMessages(t *testing.T) {
	for _, tc := range []struct {
		message string
		want    error
	}{
		{"Login incorrect", ErrLoginFailed},
		{"Authentication failed", ErrLoginFailed},
		{"Your password has expired", ErrPasswordChangeRequired},
		{"New password:", ErrPasswordChangeRequired},
	} {
		for _, ending := range []string{"", "\r\n"} {
			t.Run(tc.message+fmt.Sprintf("/%q", ending), func(t *testing.T) {
				conn := &streamTestConn{chunks: streamChunks([]byte("Password:\r\n"+tc.message+ending), 2), lastError: io.EOF}
				_, err := NewClientConn(conn, &ClientConfig{Password: "secret", Timeout: time.Second})
				if !errors.Is(err, tc.want) {
					t.Fatalf("got %v, want %v", err, tc.want)
				}
				if conn.writes.String() != "secret\r\n" {
					t.Fatalf("writes = %q", conn.writes.String())
				}
			})
		}
	}
}

func TestLoginStreamPercentFailureIsNotShell(t *testing.T) {
	conn := &streamTestConn{chunks: [][]byte{[]byte("Password:"), []byte("\r\n% Bad passwords\r\n")}}
	_, err := NewClientConn(conn, &ClientConfig{Password: "secret", Timeout: time.Second})
	if !errors.Is(err, ErrLoginFailed) {
		t.Fatalf("got %v, want ErrLoginFailed", err)
	}
}

func TestLoginStreamLongLineCannotBecomePrompt(t *testing.T) {
	data := []byte("Login:\r\nPassword:\r\n" + strings.Repeat("x", 65537) + "router#\r\nrouter#\r\nTAIL")
	checkBannerStream(t, streamChunks(data, 4096))
}

func TestLoginStreamInteractivePromptsWithoutNewlines(t *testing.T) {
	server, raw := net.Pipe()
	t.Cleanup(func() { server.Close(); raw.Close() })
	done := make(chan error, 1)
	go func() {
		defer server.Close()
		_ = server.SetDeadline(time.Now().Add(2 * time.Second))
		steps := []struct{ prompt, reply string }{
			{"Lo", ""}, {"gin:", "admin\r\n"},
			{"\x1b[31mPass", ""}, {"word:\x1b[0m", "secret\r\n"},
			{"Press ENTER to continue", "\r\n"},
		}
		for _, step := range steps {
			if _, err := io.WriteString(server, step.prompt); err != nil {
				done <- err
				return
			}
			if step.reply != "" {
				got := make([]byte, len(step.reply))
				_, err := io.ReadFull(server, got)
				if err != nil {
					done <- err
					return
				}
				if string(got) != step.reply {
					done <- fmt.Errorf("reply %q, want %q", got, step.reply)
					return
				}
			}
		}
		_, err := io.WriteString(server, "核心交换机#\r\nALARM")
		done <- err
	}()
	client, err := NewClientConn(raw, &ClientConfig{User: "admin", Password: "secret", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	output, err := io.ReadAll(client)
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != "\r\nALARM" {
		t.Fatalf("tail=%q", output)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestLoginStreamUnterminatedMessageAtTimeout(t *testing.T) {
	server, raw := net.Pipe()
	t.Cleanup(func() { server.Close(); raw.Close() })
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer server.Close()
		_ = server.SetDeadline(time.Now().Add(time.Second))
		_, _ = io.WriteString(server, "Your password has expired")
		var one [1]byte
		_, _ = server.Read(one[:])
	}()
	_, err := NewClientConn(raw, &ClientConfig{Password: "secret", Timeout: 50 * time.Millisecond})
	if !errors.Is(err, ErrPasswordChangeRequired) {
		t.Fatalf("got %v", err)
	}
	<-done
}

func TestLoginStreamColorAndUTF8Fragments(t *testing.T) {
	data := []byte("Login:\r\nPassword:\r\n\x1b]0;title\a\x1b[32m核心交换机#\x1b[0m\r\nTAIL")
	for _, size := range []int{1, 2, 5, 4096} {
		conn := &streamTestConn{chunks: streamChunks(data, size)}
		client, err := NewClientConn(conn, &ClientConfig{User: "admin", Password: "secret", Timeout: time.Second})
		if err != nil {
			t.Fatal(err)
		}
		got, err := io.ReadAll(client)
		client.Close()
		if err != nil {
			t.Fatal(err)
		}
		// A color reset arriving after the prompt is valid session data.
		got = bytes.ReplaceAll(got, []byte("\x1b[0m"), nil)
		if string(got) != "\r\nTAIL" {
			t.Fatalf("chunk %d tail=%q", size, got)
		}
	}
}

func FuzzLoginDiagnosticChunking(f *testing.F) {
	f.Add(uint16(1), uint8(0), "0")
	f.Add(uint16(4096), uint8(1), "disabled")
	f.Fuzz(func(t *testing.T, size uint16, choice uint8, suffix string) {
		if len(suffix) > 2048 {
			suffix = suffix[:2048]
		}
		suffix = strings.Map(func(r rune) rune {
			if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == ' ' {
				return r
			}
			return -1
		}, suffix)
		prefixes := []string{"Authentication failed count: ", "Password expired policy: ", "New password policy: "}
		data := []byte("Login:\r\nPassword:\r\n" + prefixes[int(choice)%len(prefixes)] + suffix + "\r\nrouter#\r\nTAIL")
		checkBannerStream(t, streamChunks(data, int(size)%4096+1))
	})
}

func BenchmarkLoginLongBanner(b *testing.B) {
	for _, size := range []int{1024, 4096, 65536} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			data := []byte(strings.Repeat("x", size) + "\r\nrouter#")
			config, _ := prepareConfig(&ClientConfig{Password: "secret"})
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			for n := 0; n < b.N; n++ {
				machine := newLoginMachine(config)
				text := newLoginText(config.maxLoginBuffer)
				for _, chunk := range streamChunks(data, 4096) {
					_, _, _, err := machine.consumeChunk(io.Discard, text, chunk, nil)
					if err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}
