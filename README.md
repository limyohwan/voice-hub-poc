# voice-hub-poc

같은 WiFi(또는 핫스팟)에 붙은 기기끼리 채널 단위로 음성을 주고받는 무전기형 음성 허브 PoC.

- Go, 외부 의존성은 protobuf 하나
- 제어 채널: TCP + protobuf (채널 JOIN, 멤버 id 발급)
- 음성: UDP 중계, 16kHz mono PCM 20ms 프레임
- 암호화는 WiFi(WPA2/WPA3)가 담당한다. 앱 레벨 암호화는 없다.

## 구조

```
클라이언트 ── TCP JoinRequest{channel} ──▶ 허브 (:9000)
           ◀── JoinResponse{member_id} ──
           (TCP 연결이 유지되는 동안 세션 유지, 끊기면 퇴장)

클라이언트 ── UDP [id(4) | seq(2) | PCM 640B] ──▶ 허브 (:9001) ──▶ 같은 채널의 다른 멤버
```

- 허브는 받은 UDP 패킷을 같은 채널의 다른 멤버에게 그대로 전달한다.
- 클라이언트는 발신자별 지터 버퍼에 seq 순서로 모았다가 20ms마다 믹싱해 재생한다. 늦게 온 패킷과 중복 패킷은 버린다.

## 실행

준비물: Go, `ffmpeg` / `ffplay` (macOS: `brew install ffmpeg`)

**1. 허브 (한 대에서)**

```bash
go run . server              # TCP 9000, UDP 9001
```

**2. 클라이언트 (각 기기에서)**

```bash
ffmpeg -f avfoundation -i ":0" -ac 1 -ar 16000 -f s16le - 2>/dev/null \
  | go run . client -host <허브 IP> -ch 1 \
  | ffplay -f s16le -ar 16000 -ch_layout mono -nodisp -
```

마이크 입력은 macOS 기준(`avfoundation`)이다. 다른 OS는 ffmpeg 입력 옵션을 바꾼다.

| 옵션 | 기본값 | 설명 |
|---|---|---|
| `-host` | `127.0.0.1` | 허브 주소 |
| `-ch` | `1` | 채널. 같은 채널끼리만 들린다 |
| `-tcp` / `-udp` | `9000` / `9001` | 포트 |
| `-jitter` | `3` | 지터 버퍼 깊이(프레임 × 20ms). 끊기면 늘리고, 지연이 크면 줄인다 |

## 테스트 팁

- 말하고 듣는 걸 확인하려면 **기기가 2대** 필요하다. 한 대에 클라이언트 두 개를 띄우면 같은 마이크를 공유해서 의미가 없다.
- 허브는 아무 기기에서나 띄워도 된다(클라이언트와 같은 기기도 가능).
- 휴대폰 핫스팟에 두 기기를 붙여도 같은 네트워크라 그대로 동작한다.
- 스피커 소리가 마이크로 다시 들어가면 하울링이 생기니 이어폰을 쓴다.

```bash
go test ./...
```

## 범위 밖 (의도적 단순화)

앱 레벨 암호화·인증, 코덱 압축(Opus 등), 에코 제거, 블루투스 전송.
