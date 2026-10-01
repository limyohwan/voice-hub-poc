package main

import (
	"encoding/binary"
	"net"
	"testing"
	"time"
)

// 같은 채널 상대에게만 중계되고, 자기 자신·다른 채널로는 가지 않아야 한다.
func TestRelaySameChannelOnly(t *testing.T) {
	h := newHub()
	srv, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	go serveVoice(srv, h)

	type peer struct {
		id   uint32
		conn *net.UDPConn
	}
	dial := func(ch string) peer {
		c, err := net.DialUDP("udp", nil, srv.LocalAddr().(*net.UDPAddr))
		if err != nil {
			t.Fatal(err)
		}
		p := peer{h.join(ch), c}
		hello := make([]byte, headerSize)
		binary.BigEndian.PutUint32(hello, p.id)
		c.Write(hello)
		return p
	}
	a, b, other := dial("1"), dial("1"), dial("2")
	time.Sleep(50 * time.Millisecond) // hello로 주소 등록 대기

	pkt := make([]byte, headerSize+4)
	binary.BigEndian.PutUint32(pkt, a.id)
	binary.BigEndian.PutUint16(pkt[4:], 1)
	copy(pkt[headerSize:], "ping")
	a.conn.Write(pkt)

	recv := func(p peer) string {
		p.conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		buf := make([]byte, 1500)
		n, err := p.conn.Read(buf)
		if err != nil {
			return ""
		}
		return string(buf[headerSize:n])
	}
	if got := recv(b); got != "ping" {
		t.Fatalf("같은 채널 b 수신 = %q, want ping", got)
	}
	if got := recv(a); got != "" {
		t.Fatalf("발신자 a에게 에코됨: %q", got)
	}
	if got := recv(other); got != "" {
		t.Fatalf("다른 채널로 새어나감: %q", got)
	}
}

// protobuf 제어 채널: 채널이 있으면 id 발급, 비어 있으면 에러 응답.
func TestJoinChannel(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go serveControl(ln, newHub())

	tests := []struct {
		ch      string
		wantID  uint32
		wantErr bool
	}{
		{ch: "1", wantID: 1},
		{ch: "", wantErr: true},
	}
	for _, tt := range tests {
		conn, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		id, err := joinChannel(conn, tt.ch)
		conn.Close()
		if (err != nil) != tt.wantErr || id != tt.wantID {
			t.Errorf("joinChannel(%q) = %d, %v; want %d, err=%v", tt.ch, id, err, tt.wantID, tt.wantErr)
		}
	}
}

func TestIsNewerWraparound(t *testing.T) {
	if !isNewer(2, 1) || isNewer(1, 2) || isNewer(5, 5) {
		t.Fatal("기본 비교 실패")
	}
	if !isNewer(0, 65535) {
		t.Fatal("랩어라운드: 0은 65535보다 새로워야 한다")
	}
}
