package main

import (
	"flag"
	"io"
	"log"
	"net"
	"time"
)

var (
	listenAddr string
	targetAddr string
	maxBytes   int
)

func init() {
	flag.StringVar(&listenAddr, "listen", "127.0.0.1:9999", "local listen address")
	flag.StringVar(&targetAddr, "target", "", "target Telnet address, for example 192.0.2.10:23")
	flag.IntVar(&maxBytes, "max-bytes", 256, "maximum bytes printed for each stream read; 0 prints lengths only")
}

func main() {
	flag.Parse()
	if targetAddr == "" {
		log.Fatal("-target is required")
	}
	if maxBytes < 0 {
		log.Fatal("-max-bytes must be zero or greater")
	}

	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		log.Fatal(err)
	}
	defer listener.Close()

	log.Printf("listening on %s, forwarding to %s", listener.Addr(), targetAddr)
	log.Printf("warning: output contains raw Telnet stream data and may expose credentials")
	log.Printf("read boundaries are user-space stream chunks, not original TCP packet boundaries")

	var connectionID uint64
	for {
		client, err := listener.Accept()
		if err != nil {
			log.Printf("accept: %v", err)
			continue
		}
		connectionID++
		go proxyConnection(connectionID, client)
	}
}

type relayResult struct {
	direction string
	err       error
}

func proxyConnection(id uint64, client net.Conn) {
	defer client.Close()
	server, err := net.DialTimeout("tcp", targetAddr, 10*time.Second)
	if err != nil {
		log.Printf("conn=%d dial target: %v", id, err)
		return
	}
	defer server.Close()

	log.Printf("conn=%d opened client=%s target=%s", id, client.RemoteAddr(), server.RemoteAddr())
	results := make(chan relayResult, 2)
	go relay(id, "client->server", server, client, results)
	go relay(id, "server->client", client, server, results)

	first := <-results
	_ = client.Close()
	_ = server.Close()
	second := <-results
	log.Printf("conn=%d closed %s=%v %s=%v", id,
		first.direction, first.err, second.direction, second.err)
}

func relay(id uint64, direction string, dst net.Conn, src net.Conn, results chan<- relayResult) {
	buffer := make([]byte, 32*1024)
	for {
		n, readErr := src.Read(buffer)
		if n > 0 {
			logChunk(id, direction, buffer[:n])
			if writeErr := writeFull(dst, buffer[:n]); writeErr != nil {
				results <- relayResult{direction: direction, err: writeErr}
				return
			}
		}
		if readErr != nil {
			results <- relayResult{direction: direction, err: readErr}
			return
		}
	}
}

func logChunk(id uint64, direction string, data []byte) {
	if maxBytes == 0 {
		log.Printf("conn=%d %s len=%d", id, direction, len(data))
		return
	}
	sample := data
	suffix := ""
	if len(sample) > maxBytes {
		sample = sample[:maxBytes]
		suffix = " ..."
	}
	log.Printf("conn=%d %s len=%d data=% x%s", id, direction, len(data), sample, suffix)
}

func writeFull(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := writer.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
