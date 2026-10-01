package main

import (
	"encoding/binary"
	"sync"
)

const maxBufferedFrames = 50 // 1초. 넘게 쌓이면 비정상 적체로 보고 다시 버퍼링한다

// jitterBuffer: 발신자별로 프레임을 seq 순서로 모아 두었다가 20ms마다 하나씩 꺼내 믹싱한다.
// depth 프레임이 쌓여야 재생을 시작하고(초기 지연 = depth × 20ms), 버퍼가 비면 다시 쌓일 때까지 기다린다.
// ponytail: 발신자 상태를 지우지 않음(나간 사람만큼 수백 바이트 누적), 장시간 운영이면 마지막 수신 시각으로 정리
type jitterBuffer struct {
	mu      sync.Mutex
	depth   int
	streams map[uint32]*stream
}

type stream struct {
	frames  map[uint16][]byte
	next    uint16 // 다음에 재생할 seq
	started bool   // next가 유효한지 (한 번이라도 재생을 시작했는지)
	playing bool
}

func newJitterBuffer(depth int) *jitterBuffer {
	return &jitterBuffer{depth: depth, streams: map[uint32]*stream{}}
}

func (j *jitterBuffer) push(from uint32, seq uint16, pcm []byte) {
	j.mu.Lock()
	defer j.mu.Unlock()
	s, ok := j.streams[from]
	if !ok {
		s = &stream{frames: map[uint16][]byte{}}
		j.streams[from] = s
	}
	if s.started && !isNewer(seq, s.next-1) {
		return // 재생 시점이 이미 지난 패킷(늦게 도착했거나 중복)
	}
	if len(s.frames) >= maxBufferedFrames {
		*s = stream{frames: map[uint16][]byte{}}
	}
	s.frames[seq] = append([]byte(nil), pcm...) // 수신 버퍼는 재사용되므로 복사
}

// pop: 재생할 20ms 프레임 하나(s16le)를 돌려준다. 아무도 말하지 않으면 무음.
func (j *jitterBuffer) pop() []byte {
	j.mu.Lock()
	defer j.mu.Unlock()
	var mix [frameBytes / 2]int32
	for _, s := range j.streams {
		if !s.playing {
			if len(s.frames) < j.depth {
				continue
			}
			s.next, s.started, s.playing = oldestSeq(s.frames), true, true
		}
		f, ok := s.frames[s.next]
		if !ok && len(s.frames) == 0 {
			s.playing = false // 버퍼 고갈(발화 종료 또는 네트워크 끊김): 다시 depth만큼 쌓일 때까지 대기
			continue
		}
		if ok {
			delete(s.frames, s.next)
			for i := 0; i < len(mix) && 2*i+1 < len(f); i++ {
				mix[i] += int32(int16(binary.LittleEndian.Uint16(f[2*i:])))
			}
		}
		// ok=false면 해당 프레임 유실 → 무음으로 채우고 다음 seq로 진행
		// ponytail: 유실 구간은 무음, 끊김이 거슬리면 직전 프레임 반복(PLC) 추가
		s.next++
	}
	out := make([]byte, frameBytes)
	for i, v := range mix {
		binary.LittleEndian.PutUint16(out[2*i:], uint16(int16(max(-32768, min(32767, v)))))
	}
	return out
}

// oldestSeq: 랩어라운드를 고려해 가장 오래된 seq를 찾는다.
func oldestSeq(frames map[uint16][]byte) uint16 {
	var oldest uint16
	first := true
	for seq := range frames {
		if first || isNewer(oldest, seq) {
			oldest, first = seq, false
		}
	}
	return oldest
}
