package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
)

type proxyData struct {
	mu           sync.Mutex
	blockedSites map[string]bool
	cache        map[string][]byte
	logs         map[string]string
}

func startProxy(sharedData *proxyData, program *tea.Program) {
	listener, err := net.Listen("tcp", ":4000")
	if err != nil {
		log.Fatal("[Proxy Error]", err)
	}
	defer listener.Close()

	program.Send(LogMsg{Level: "INFO", Text: "Proxy listening on :4000", Timestamp: time.Now()})

	for {
		conn, err := listener.Accept()
		if err != nil {
			program.Send(LogMsg{Level: "ERROR", Text: "Accept error: " + err.Error(), Timestamp: time.Now()})
			continue
		}
		go handleConnection(conn, sharedData, program)
	}
}

func closeConn(conn net.Conn) {
	if err := conn.Close(); err != nil {
		log.Println("[Conn Close Error]:", err.Error())
	}
}

func handleConnection(clientConn net.Conn, sharedData *proxyData, program *tea.Program) {
	start := time.Now()
	defer closeConn(clientConn)

	// http.ReadRequest reads exactly one HTTP request without blocking for EOF
	req, err := http.ReadRequest(bufio.NewReader(clientConn))
	if err != nil {
		if err != io.EOF {
			program.Send(LogMsg{Level: "ERROR", Text: "Parse error: " + err.Error(), Timestamp: start})
		}
		return
	}
	defer req.Body.Close()

	method := req.Method
	host := req.Host
	path := req.URL.RequestURI()

	// Ensure host has a port for dialing
	dialHost := host
	if !strings.Contains(dialHost, ":") {
		dialHost += ":80"
	}

	// Blocking check — strip port so "example.com:443" matches "example.com"
	blockHost := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		blockHost = h
	}
	sharedData.mu.Lock()
	isBlocked := sharedData.blockedSites[blockHost]
	sharedData.mu.Unlock()
	if isBlocked {
		clientConn.Write([]byte("HTTP/1.1 403 Forbidden\r\nContent-Length: 0\r\n\r\n"))
		program.Send(RequestMsg{Method: method, Host: host, Path: path, Blocked: true, Duration: time.Since(start), Timestamp: start})
		program.Send(LogMsg{Level: "BLOCK", Text: fmt.Sprintf("Blocked: %s %s", method, host), Timestamp: start})
		return
	}

	// CONNECT — HTTPS tunnel, no caching
	if method == "CONNECT" {
		serverConn, err := net.Dial("tcp", dialHost)
		if err != nil {
			clientConn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n"))
			program.Send(LogMsg{Level: "ERROR", Text: "CONNECT dial error: " + err.Error(), Timestamp: start})
			return
		}

		clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		program.Send(RequestMsg{Method: method, Host: host, Path: path, Duration: time.Since(start), Timestamp: start})
		go io.Copy(serverConn, clientConn)
		io.Copy(clientConn, serverConn)
		serverConn.Close()
		return
	}

	// Cache lookup
	cacheKey := method + "|" + host + "|" + path
	if method == "GET" || method == "HEAD" {
		sharedData.mu.Lock()
		cachedResponse, cacheHit := sharedData.cache[cacheKey]
		sharedData.mu.Unlock()

		if cacheHit {
			clientConn.Write(cachedResponse)
			duration := time.Since(start)
			program.Send(RequestMsg{
				Method: method, Host: host, Path: path,
				CacheHit: true, Duration: duration, Bytes: len(cachedResponse),
				Timestamp: start,
			})
			program.Send(LogMsg{
				Level:     "CACHE",
				Text:      fmt.Sprintf("Hit: %s %s (%dµs, %dB saved)", method, host, duration.Microseconds(), len(cachedResponse)),
				Timestamp: start,
			})
			return
		}
	}

	// Fresh fetch
	serverConn, err := net.Dial("tcp", dialHost)
	if err != nil {
		clientConn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n"))
		program.Send(LogMsg{Level: "ERROR", Text: "Dial error: " + err.Error(), Timestamp: start})
		return
	}
	defer serverConn.Close()

	// req.Write sends a properly formatted HTTP/1.1 request to the origin server
	if err = req.Write(serverConn); err != nil {
		program.Send(LogMsg{Level: "ERROR", Text: "Server write error: " + err.Error(), Timestamp: start})
		return
	}

	// http.ReadResponse reads exactly one response, respecting Content-Length /
	// Transfer-Encoding, so it returns as soon as the body is complete — unlike
	// io.ReadAll which blocks until the TCP connection closes (keep-alive hangs).
	resp, err := http.ReadResponse(bufio.NewReader(serverConn), req)
	if err != nil {
		program.Send(LogMsg{Level: "ERROR", Text: "Server response error: " + err.Error(), Timestamp: start})
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		program.Send(LogMsg{Level: "ERROR", Text: "Server body read error: " + err.Error(), Timestamp: start})
		return
	}

	// Re-serialise into bytes for caching and forwarding
	resp.Body = io.NopCloser(bytes.NewReader(body))
	var buf bytes.Buffer
	resp.Write(&buf)
	serverResponse := buf.Bytes()

	if _, err = clientConn.Write(serverResponse); err != nil {
		program.Send(LogMsg{Level: "ERROR", Text: "Client write error: " + err.Error(), Timestamp: start})
		return
	}

	duration := time.Since(start)

	if method == "GET" || method == "HEAD" {
		sharedData.mu.Lock()
		sharedData.cache[cacheKey] = serverResponse
		sharedData.mu.Unlock()
	}

	program.Send(RequestMsg{
		Method: method, Host: host, Path: path,
		CacheHit: false, Duration: duration, Bytes: len(serverResponse),
		Timestamp: start,
	})

	program.Send(LogMsg{
		Level:     "INFO",
		Text:      fmt.Sprintf("Fetched: %s %s (%dµs, %dB)", method, host, duration.Microseconds(), len(serverResponse)),
		Timestamp: start,
	})
}
