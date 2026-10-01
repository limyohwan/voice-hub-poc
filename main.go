// 음성 허브 PoC: protobuf 제어 채널(TCP, JOIN) + UDP 음성 중계.
// 암호화는 허브 AP의 WiFi(WPA2/WPA3)가 담당한다 — 앱 레벨 암호화 없음.
//
// 실행:
//
//	서버:    go run . server
//	클라이언트: ffmpeg -f avfoundation -i ":0" -ac 1 -ar 16000 -f s16le - 2>/dev/null \
//	           | go run . client -ch 1 \
//	           | ffplay -f s16le -ar 16000 -ch_layout mono -nodisp -
package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"flag"
	"io"
	"log"
	"net"
	"os"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protodelim"

	pb "voice-hub-poc/controlpb"
)

const (
	headerSize    = 6   // id(uint32) + seq(uint16)
	frameBytes    = 640 // 16kHz mono s16le 20ms
	frameDuration = 20 * time.Millisecond
)

type member struct {
	ch   string
	addr *net.UDPAddr
}

type hub struct {
	mu      sync.Mutex
	nextID  uint32
	members map[uint32]*member
}

func newHub() *hub { return &hub{members: map[uint32]*member{}} }

func (h *hub) join(ch string) uint32 {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.nextID++
	h.members[h.nextID] = &member{ch: ch}
	return h.nextID
}

func (h *hub) leave(id uint32) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.members, id)
}

// route는 보낸 쪽의 UDP 주소를 기록하고, 같은 채널의 다른 멤버 주소를 돌려준다.
// ponytail: 전체 멤버 선형 탐색, 채널당 수십 명을 넘으면 채널별 맵으로 분리
func (h *hub) route(id uint32, from *net.UDPAddr) []*net.UDPAddr {
	h.mu.Lock()
	defer h.mu.Unlock()
	sender, ok := h.members[id]
	if !ok {
		return nil // JOIN 하지 않은 id는 버린다
	}
	sender.addr = from
	var out []*net.UDPAddr
	for mid, m := range h.members {
		if mid != id && m.ch == sender.ch && m.addr != nil {
			out = append(out, m.addr)
		}
	}
	return out
}

const joinTimeout = 10 * time.Second // 접속 후 JoinRequest까지 허용 시간

// serveControl: JoinRequest → JoinResponse. 연결이 끊기면 멤버에서 제거한다.
func serveControl(ln net.Listener, h *hub) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go func() {
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(joinTimeout))
			req := &pb.JoinRequest{}
			if err := protodelim.UnmarshalFrom(bufio.NewReader(conn), req); err != nil {
				log.Printf("join 수신 실패 from=%s: %v", conn.RemoteAddr(), err)
				return
			}
			if req.Channel == "" {
				protodelim.MarshalTo(conn, &pb.JoinResponse{Error: "channel is required"})
				return
			}
			id := h.join(req.Channel)
			defer h.leave(id)
			log.Printf("join id=%d ch=%s from=%s", id, req.Channel, conn.RemoteAddr())
			if _, err := protodelim.MarshalTo(conn, &pb.JoinResponse{MemberId: id}); err != nil {
				return
			}
			conn.SetDeadline(time.Time{})
			io.Copy(io.Discard, conn) // 연결 유지 = 세션 유지
			log.Printf("leave id=%d", id)
		}()
	}
}

// joinChannel: 제어 연결로 JoinRequest를 보내고 발급된 member id를 받는다.
func joinChannel(conn net.Conn, ch string) (uint32, error) {
	if _, err := protodelim.MarshalTo(conn, &pb.JoinRequest{Channel: ch}); err != nil {
		return 0, err
	}
	resp := &pb.JoinResponse{}
	if err := protodelim.UnmarshalFrom(bufio.NewReader(conn), resp); err != nil {
		return 0, err
	}
	if resp.Error != "" {
		return 0, errors.New(resp.Error)
	}
	return resp.MemberId, nil
}

func serveVoice(conn *net.UDPConn, h *hub) {
	buf := make([]byte, 1500)
	for {
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if n < headerSize {
			continue
		}
		for _, to := range h.route(binary.BigEndian.Uint32(buf), from) {
			conn.WriteToUDP(buf[:n], to)
		}
	}
}

func runServer(tcpAddr, udpAddr string) {
	h := newHub()
	ln, err := net.Listen("tcp", tcpAddr)
	if err != nil {
		log.Fatal(err)
	}
	ua, _ := net.ResolveUDPAddr("udp", udpAddr)
	uc, err := net.ListenUDP("udp", ua)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("control tcp %s, voice udp %s", tcpAddr, udpAddr)
	go serveControl(ln, h)
	serveVoice(uc, h)
}

// isNewer: uint16 seq 랩어라운드를 고려한 비교
func isNewer(seq, last uint16) bool { return int16(seq-last) > 0 }

func runClient(host, tcpPort, udpPort, ch string, jitterDepth int) {
	ctrl, err := net.Dial("tcp", net.JoinHostPort(host, tcpPort))
	if err != nil {
		log.Fatal(err)
	}
	id, err := joinChannel(ctrl, ch)
	if err != nil {
		log.Fatalf("join 실패: %v", err)
	}
	log.Printf("joined ch=%s id=%d", ch, id)

	voice, err := net.Dial("udp", net.JoinHostPort(host, udpPort))
	if err != nil {
		log.Fatal(err)
	}

	// 수신: 지터 버퍼에 넣기만 하고, 재생은 아래 20ms 틱이 담당한다.
	jb := newJitterBuffer(jitterDepth)
	go func() {
		buf := make([]byte, 1500)
		for {
			n, err := voice.Read(buf)
			if err != nil {
				log.Fatal(err)
			}
			if n < headerSize {
				continue
			}
			jb.push(binary.BigEndian.Uint32(buf), binary.BigEndian.Uint16(buf[4:]), buf[headerSize:n])
		}
	}()
	// 재생: 20ms마다 믹싱된 프레임 하나를 stdout에 쓴다(아무도 말하지 않으면 무음).
	// ponytail: 송신 측 오디오 클럭과 수신 측 타이머가 조금씩 어긋남, 쌓이거나 비면 버퍼 리셋으로만 흡수
	go func() {
		for range time.Tick(frameDuration) {
			os.Stdout.Write(jb.pop())
		}
	}()

	// 송신: 헤더만 있는 패킷으로 서버에 UDP 주소를 먼저 알린 뒤, stdin PCM을 20ms 단위로 전송
	pkt := make([]byte, headerSize+frameBytes)
	binary.BigEndian.PutUint32(pkt, id)
	voice.Write(pkt[:headerSize])
	for seq := uint16(1); ; seq++ {
		if _, err := io.ReadFull(os.Stdin, pkt[headerSize:]); err != nil {
			select {} // 입력이 끝나도 수신은 계속
		}
		binary.BigEndian.PutUint16(pkt[4:], seq)
		voice.Write(pkt)
	}
}

func main() {
	if len(os.Args) < 2 {
		log.Fatal("usage: server | client -ch <채널>")
	}
	fs := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	host := fs.String("host", "127.0.0.1", "서버 주소 (client)")
	tcp := fs.String("tcp", "9000", "제어 TCP 포트")
	udp := fs.String("udp", "9001", "음성 UDP 포트")
	ch := fs.String("ch", "1", "채널 (client)")
	jitter := fs.Int("jitter", 3, "지터 버퍼 깊이(프레임, ×20ms). 끊기면 늘리고 지연이 크면 줄인다 (client)")
	fs.Parse(os.Args[2:])

	switch os.Args[1] {
	case "server":
		runServer(":"+*tcp, ":"+*udp)
	case "client":
		if *jitter < 1 {
			log.Fatal("-jitter는 1 이상이어야 합니다")
		}
		runClient(*host, *tcp, *udp, *ch, *jitter)
	default:
		log.Fatal("usage: server | client -ch <채널>")
	}
}
