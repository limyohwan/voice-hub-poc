package main

import (
	"encoding/binary"
	"testing"
)

// step: pop=false면 push(from, seq, 모든 샘플이 val인 프레임), pop=true면 pop() 결과 샘플이 want인지 확인
type step struct {
	from uint32
	seq  uint16
	val  int16
	pop  bool
	want int16
}

func push(from uint32, seq uint16, val int16) step { return step{from: from, seq: seq, val: val} }
func popWant(want int16) step                      { return step{pop: true, want: want} }

func frameOf(val int16) []byte {
	f := make([]byte, frameBytes)
	for i := 0; i < frameBytes; i += 2 {
		binary.LittleEndian.PutUint16(f[i:], uint16(val))
	}
	return f
}

func TestJitterBuffer(t *testing.T) {
	tests := []struct {
		name  string
		depth int
		steps []step
	}{
		{"순서가 뒤바뀌어 도착해도 seq 순으로 재생", 2, []step{
			push(1, 2, 20), push(1, 1, 10), push(1, 3, 30),
			popWant(10), popWant(20), popWant(30),
		}},
		{"depth만큼 쌓이기 전에는 무음", 3, []step{
			push(1, 1, 10), push(1, 2, 20), popWant(0),
			push(1, 3, 30), popWant(10),
		}},
		{"유실된 프레임은 무음으로 채우고 진행", 1, []step{
			push(1, 1, 10), push(1, 3, 30),
			popWant(10), popWant(0), popWant(30),
		}},
		{"재생 시점이 지난 늦은·중복 패킷은 버림", 1, []step{
			push(1, 1, 10), popWant(10),
			push(1, 1, 99), push(1, 2, 20), popWant(20),
		}},
		{"두 사람 동시 발화는 합산, 범위를 넘으면 클리핑", 1, []step{
			push(1, 1, 1000), push(2, 1, -300), popWant(700),
			push(1, 2, 30000), push(2, 2, 10000), popWant(32767),
		}},
		{"버퍼가 비면 다시 depth만큼 쌓일 때까지 대기", 2, []step{
			push(1, 1, 10), push(1, 2, 20), popWant(10), popWant(20),
			popWant(0), push(1, 3, 30), popWant(0), push(1, 4, 40), popWant(30),
		}},
		{"seq 랩어라운드(65535 → 0)", 2, []step{
			push(1, 0, 20), push(1, 65535, 10),
			popWant(10), popWant(20),
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jb := newJitterBuffer(tt.depth)
			for i, s := range tt.steps {
				if !s.pop {
					jb.push(s.from, s.seq, frameOf(s.val))
					continue
				}
				out := jb.pop()
				if len(out) != frameBytes {
					t.Fatalf("step %d: 프레임 길이 %d, want %d", i, len(out), frameBytes)
				}
				if got := int16(binary.LittleEndian.Uint16(out)); got != s.want {
					t.Fatalf("step %d: 샘플 = %d, want %d", i, got, s.want)
				}
			}
		})
	}
}
