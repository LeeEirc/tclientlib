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

	"github.com/LeeEirc/tclientlib"
)

var (
	IpAddr   string
	Port     string
	password string
	username string
)

func init() {
	flag.StringVar(&IpAddr, "ip", "127.0.0.1", " telnet ip")
	flag.StringVar(&Port, "port", "23", "telnet port")
	flag.StringVar(&username, "username", "", "telnet user")
	flag.StringVar(&password, "password", "", " telnet password")
}

func main() {
	flag.Parse()
	conf := tclientlib.ClientConfig{
		User:     username,
		Password: password,
		Timeout:  30 * time.Second,
	}
	client, err := tclientlib.Dial("tcp", net.JoinHostPort(IpAddr, Port), &conf)
	if err != nil {
		log.Panicln(err)
	}
	sigChan := make(chan struct{}, 1)

	go func() {
		_, _ = io.Copy(os.Stdin, client)
		sigChan <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(client, os.Stdout)
		sigChan <- struct{}{}
	}()

	sigwinchCh := make(chan os.Signal, 1)
	signal.Notify(sigwinchCh, syscall.SIGWINCH)
	for {
		select {
		case <-sigChan:
			return

		// 阻塞读取
		case sigwinch := <-sigwinchCh:
			if sigwinch == nil {
				return
			}
			if err != nil {
				log.Println("Unable to send window-change request.")
				continue
			}
		}
	}
}