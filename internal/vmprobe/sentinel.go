package vmprobe

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
)

// Hit is one connection or datagram received by a sentinel.
type Hit struct {
	Time     time.Time `json:"time"`
	Sentinel string    `json:"sentinel"`
	Proto    string    `json:"proto"`
	Local    string    `json:"local"`
	Remote   string    `json:"remote"`
	Payload  string    `json:"payload"`
}

// Sentinel listens on all host addresses and records every hit.
type Sentinel struct {
	Name  string
	Ports []int
	Log   io.Writer

	mu  sync.Mutex
	enc *json.Encoder
}

func (s *Sentinel) record(h Hit) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.enc == nil {
		s.enc = json.NewEncoder(s.Log)
	}
	_ = s.enc.Encode(h)
}

func (s *Sentinel) banner() []byte {
	return []byte("JLS1 " + s.Name + "\n")
}

// Run serves TCP and UDP on every port until ctx is cancelled.
func (s *Sentinel) Run(ctx context.Context) error {
	var lc net.ListenConfig
	var closers []io.Closer
	defer func() {
		for _, c := range closers {
			_ = c.Close()
		}
	}()
	var wg sync.WaitGroup
	for _, port := range s.Ports {
		addr := ":" + strconv.Itoa(port)
		ln, err := lc.Listen(ctx, "tcp", addr)
		if err != nil {
			return fmt.Errorf("listen tcp %s: %w", addr, err)
		}
		closers = append(closers, ln)
		pc, err := lc.ListenPacket(ctx, "udp", addr)
		if err != nil {
			return fmt.Errorf("listen udp %s: %w", addr, err)
		}
		closers = append(closers, pc)
		wg.Add(2)
		go func() { defer wg.Done(); s.serveTCP(ln) }()
		go func() { defer wg.Done(); s.serveUDP(pc) }()
	}
	<-ctx.Done()
	for _, c := range closers {
		_ = c.Close()
	}
	wg.Wait()
	return nil
}

func (s *Sentinel) serveTCP(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		go func() {
			defer func() { _ = conn.Close() }()
			_ = conn.SetDeadline(time.Now().Add(DefaultTimeout))
			line, _ := bufio.NewReader(io.LimitReader(conn, 512)).ReadString('\n')
			s.record(Hit{
				Time: time.Now().UTC(), Sentinel: s.Name, Proto: "tcp",
				Local: conn.LocalAddr().String(), Remote: conn.RemoteAddr().String(),
				Payload: firstLine([]byte(line)),
			})
			_, _ = conn.Write(s.banner())
		}()
	}
}

func (s *Sentinel) serveUDP(pc net.PacketConn) {
	buf := make([]byte, 2048)
	for {
		n, from, err := pc.ReadFrom(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		s.record(Hit{
			Time: time.Now().UTC(), Sentinel: s.Name, Proto: "udp",
			Local: pc.LocalAddr().String(), Remote: from.String(),
			Payload: firstLine(buf[:n]),
		})
		_, _ = pc.WriteTo(s.banner(), from)
	}
}

// ReadHits decodes a sentinel JSONL log.
func ReadHits(r io.Reader) ([]Hit, error) {
	var hits []Hit
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		if len(sc.Bytes()) == 0 {
			continue
		}
		var h Hit
		if err := json.Unmarshal(sc.Bytes(), &h); err != nil {
			return nil, fmt.Errorf("decode hit: %w", err)
		}
		hits = append(hits, h)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("scan hits: %w", err)
	}
	return hits, nil
}
