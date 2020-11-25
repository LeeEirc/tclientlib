package main

import (
	"flag"
	"log"
	"net"
	"sync"
	"unicode"

	"github.com/LeeEirc/tclientlib"
)

var (
	IpAddr string
	Port   string

	mux sync.Mutex
)

func init() {
	flag.StringVar(&IpAddr, "ip", "127.0.0.1", "proxy telnet ip ")
	flag.StringVar(&Port, "port", "23", "proxy telnet port")
}

func main() {
	flag.Parse()
	ln, err := net.Listen("tcp", "0.0.0.0:0")
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
		log.Println("handler--")
		handler(conn)

	}

}

func handler(con net.Conn) {
	addr := net.JoinHostPort(IpAddr, Port)
	dstCon, err := net.Dial("tcp", addr)
	if err != nil {
		return
	}
	defer dstCon.Close()

	go func() {
		readBuf := make([]byte, 1024)

		for {
			nr, err := dstCon.Read(readBuf)
			if err != nil {
				log.Println(err)
				break
			}
			mux.Lock()
			log.Println("server send: ", ConvertHumanText(readBuf[:nr]))
			_, _ = con.Write(readBuf[:nr])
			mux.Unlock()
		}
		_ = con.Close()
	}()

	writeBuf := make([]byte, 1024)
	for {
		wr, err := con.Read(writeBuf)
		if err != nil {
			log.Println(err)
			break
		}
		mux.Lock()
		log.Println("client send: ", ConvertHumanText(writeBuf[:wr]))
		_, _ = dstCon.Write(writeBuf[:wr])
		mux.Unlock()
	}
}

func ConvertHumanText(p []byte) []string {
	humanText := make([]string, 0, len(p))
	for _, v := range p {
		if txt, ok := tclientlib.CodeTOASCII[v]; ok {
			humanText = append(humanText, txt)
		} else {
			if unicode.IsPrint(rune(v)) {
				humanText = append(humanText, string(v))
				continue
			}
			humanText = append(humanText, "unknown code")
		}
	}
	return humanText
}
