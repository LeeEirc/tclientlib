package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"unicode"
	"unicode/utf8"

	"github.com/LeeEirc/tclientlib"
)

var (
	IpAddr string
	Port   string
)

func init() {
	flag.StringVar(&IpAddr, "ip", "127.0.0.1", "proxy telnet ip ")
	flag.StringVar(&Port, "port", "23", "proxy telnet port")
}

func main() {
	flag.Parse()
	ln, err := net.Listen("tcp", "0.0.0.0:9999")
	if err != nil {
		log.Fatal(err)
	}
	log.Println("监听端口：", ln.Addr().String())
	log.Println("代理的telnet服务地址：", net.JoinHostPort(IpAddr, Port))
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Println(err)
			continue
		}
		go handler(conn)
	}

}

func handler(con net.Conn) {
	defer con.Close()
	addr := net.JoinHostPort(IpAddr, Port)
	dstCon, err := net.Dial("tcp", addr)
	if err != nil {
		log.Println(err)
		return
	}
	defer dstCon.Close()
	srvChan := make(chan []byte)
	clientChan := make(chan []byte)
	done := make(chan struct{}, 2)
	finished := make(chan struct{})
	go func() {
		readBuf := make([]byte, 1024)
		for {
			nr, err := dstCon.Read(readBuf)
			if err != nil {
				log.Println(err)
				break
			}
			srvChan <- readBuf[:nr]
			<-finished

		}
		done <- struct{}{}
		log.Println("close dstCon")
	}()

	go func() {
		writeBuf := make([]byte, 1024)
		for {
			wr, err := con.Read(writeBuf)
			if err != nil {
				log.Println(err)
				break
			}
			clientChan <- writeBuf[:wr]
			<-finished
		}
		done <- struct{}{}
		log.Println("close con")
	}()

	for {
		var (
			from      string
			humanText []string
		)
		select {
		case <-done:
			return
		case p := <-srvChan:
			humanText = ConvertHumanText(p)
			from = "server send"
			_, _ = con.Write(p)

		case p := <-clientChan:
			humanText = ConvertHumanText(p)
			from = "client send"
			_, _ = dstCon.Write(p)

		}
		finished <- struct{}{}
		log.Printf("%s: len(%d) %v", from, len(humanText), humanText)
	}
}

func ConvertHumanText(p []byte) []string {
	humanText := make([]string, 0, len(p))
	if len(p) == 0 {
		return humanText
	}
	remain := p
	for len(remain) > 0 {
		var (
			packet tclientlib.OptionPacket
			ok     bool
			code   rune
		)
		if packet, remain, ok = tclientlib.ReadOptionPacket(remain); ok {
			humanText = append(humanText, packet.String())
			continue
		}
		if code, remain = readRunePacket(remain); code != utf8.RuneError {
			if unicode.IsPrint(code) {
				humanText = append(humanText, string(code))
			} else {
				humanText = append(humanText, fmt.Sprintf("%q", code))
			}
			continue
		}
		log.Println("unknown remain data and break: ", remain)
		break
	}
	return humanText
}

func readRunePacket(p []byte) (code rune, rest []byte) {
	r, l := utf8.DecodeRune(p)
	return r, p[l:]
}
