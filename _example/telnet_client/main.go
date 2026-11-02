package main

import (
	"flag"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/term"

	"github.com/LeeEirc/tclientlib"
)

var (
	ip       string
	port     string
	username string
	password string
)

func init() {
	flag.StringVar(&ip, "ip", "127.0.0.1", "telnet IP")
	flag.StringVar(&port, "port", "23", "telnet port")
	flag.StringVar(&username, "username", "", "telnet username")
	flag.StringVar(&password, "password", "", "telnet password")
}

func main() {
	flag.Parse()
	fd := int(os.Stdin.Fd())
	state, err := term.MakeRaw(fd)
	if err != nil {
		log.Fatal(err)
	}
	defer term.Restore(fd, state)

	width, height, _ := term.GetSize(fd)
	client, err := tclientlib.Dial("tcp", net.JoinHostPort(ip, port), &tclientlib.ClientConfig{
		User: username, Password: password, Timeout: 30 * time.Second,
		TerminalType: "xterm", Width: width, Height: height,
	})
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()

	done := make(chan struct{}, 2)
	go func() {
		_, _ = io.Copy(os.Stdout, client)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(client, os.Stdin)
		done <- struct{}{}
	}()

	resize := make(chan os.Signal, 1)
	signal.Notify(resize, syscall.SIGWINCH)
	for {
		select {
		case <-done:
			return
		case <-resize:
			width, height, _ = term.GetSize(fd)
			if err := client.WindowChange(height, width); err != nil {
				log.Printf("window change: %v", err)
			}
		}
	}
}
