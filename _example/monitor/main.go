package main

import (
	"bytes"
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
	log.Println(ln.Addr().String())
	log.Println(IpAddr, Port)
	for {
		conn, err := ln.Accept()
		if err != nil {
			log.Println(err)
			continue
		}
		handler(conn)

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
	go func() {
		readBuf := make([]byte, 1024)
		for {
			nr, err := dstCon.Read(readBuf)
			if err != nil {
				log.Println(err)
				break
			}
			srvChan <- readBuf[:nr]

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
		}
		done <- struct{}{}
		log.Println("close con")
	}()

	for {
		select {
		case <-done:
			return
		case p := <-srvChan:
			log.Println("server send: ", ConvertHumanText(p))
			_, _ = con.Write(p)
		case p := <-clientChan:
			log.Println("client send: ", ConvertHumanText(p))
			_, _ = dstCon.Write(p)
		}
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
		if packet, remain, ok = readOptionPacket(remain); ok {
			humanText = append(humanText, packet.String())
			continue
		}
		if code, remain, ok = readRunePacket(remain); ok {
			if unicode.IsPrint(code) {
				humanText = append(humanText, string(code))
			} else {
				log.Printf("could not print rune: %q", code)
				humanText = append(humanText, fmt.Sprintf("%q", code))
			}
			continue
		}
		log.Println("unknown remain data and break: ", remain)
		break
	}
	return humanText
}

func readOptionPacket(p []byte) (packet tclientlib.OptionPacket, rest []byte, ok bool) {
	if len(p) == 0 {
		return
	}
	if p[0] == tclientlib.IAC && len(p) >= 3 {
		packet.OptionCode = p[1]
		packet.CommandCode = p[2]
		switch p[1] {
		case tclientlib.WILL, tclientlib.WONT, tclientlib.DO, tclientlib.DONT:
			log.Printf("option packet: %s\n", packet)
			return packet, p[3:], true
		case tclientlib.SB:
			remain := p[3:]
			index := bytes.IndexByte(remain, tclientlib.SE)
			if index < 0 {
				log.Panicf("%d %v", index, index)
			}
			packet.Parameters = make([]byte, len(remain[:index])-1)
			copy(packet.Parameters, remain[:index])
			log.Printf("option packet: %s\n", packet)
			return packet, remain[index+1:], true
		default:
			log.Panicf("%v", p[1])
		}
	}
	return packet, p, false
}

func readRunePacket(p []byte) (code rune, rest []byte, ok bool) {
	if !utf8.FullRune(p) {
		return utf8.RuneError, p, false
	}
	r, l := utf8.DecodeRune(p)
	if r == utf8.RuneError {
		return utf8.RuneError, p, false
	}
	return r, p[l:], true
}
