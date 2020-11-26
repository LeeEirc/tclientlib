package main

import (
	"flag"
	"log"
	"net"
	"unicode"

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
