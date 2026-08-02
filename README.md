# Home Brain RTC Gateway

Home Brain RTC Gateway, Alexa Smart Home `Alexa.RTCSessionController` SDP offer'larını Pion WebRTC ile cevaplayan ve Echo mikrofonundan gelen Opus RTP paketlerini doğrudan OGG/Opus dosyalarına kaydeden dar kapsamlı bir MVP'dir.

Bu repo STT, OpenAI/LLM, TTS, VAD, keyword detection, ses temizleme, WAV dönüşümü, video işleme, MediaMTX ve TURN içermez.

> Güvenlik uyarısı: Bu MVP signaling için bearer token korumalı düz HTTP kullanır. Bu yalnız kontrollü/private MVP kullanımı içindir. Daha geniş internet-facing production kullanımı TLS, reverse proxy, rate limiting ve ek ağ sertleştirmesi gerektirir.

## Mimari

```text
Alexa directive
    │
    ▼
AWS Lambda (lambda/index.mjs)
    │  HTTP bearer signaling
    ▼
Go API :8080 ── SessionManager ── Pion PeerConnection
                                      │
Echo device ◄──── direct WebRTC UDP ──┘
    microphone Opus RTP ──────────────► OGG writer ── recordings/*.ogg
```

Signaling yolu Alexa → Lambda → Go servisidir. SDP answer Lambda üzerinden Alexa'ya döner. Medya Lambda'dan geçmez; Echo ile VPS üzerindeki Pion arasında UDP 40000–40020 üzerinden doğrudan akar.

Her oturum sessiz bir local Opus track ekleyerek full-duplex audio negotiate eder, local sender RTCP paketlerini tüketir ve ilk remote Opus audio track'i OGG'ye yazar. Decode veya transcoding yapılmaz.

`SessionConnected` yalnız gözlemsel ve idempotent bir işarettir. Alexa bu directive'i göndermese bile PeerConnection, `OnTrack`, kayıt, TTL ve cleanup çalışır. Offer video m-line içerirse video rejected/inactive cevaplanır; desteklenen Opus audio devam eder.

## HTTP API

| Method | Path | Auth | Sonuç |
|---|---|---|---|
| GET | `/healthz` | Hayır | `{"status":"ok"}` |
| POST | `/v1/rtc/sessions` | Bearer | Offer alır, complete ICE SDP answer döner |
| POST | `/v1/rtc/sessions/{sessionId}/connected` | Bearer | Gözlemsel connected işareti |
| DELETE | `/v1/rtc/sessions/{sessionId}` | Bearer | PeerConnection ve OGG writer'ı kapatır |

Tüm `/v1` istekleri `Authorization: Bearer <SESSION_API_TOKEN>` gerektirir. Body üst sınırı 1 MiB'dir. Aynı aktif `sessionId` yeniden oluşturulursa mevcut oturum değiştirilmez ve `409 Conflict` döner.

## Environment variables

| Değişken | Varsayılan | Açıklama |
|---|---:|---|
| `HTTP_ADDR` | `:8080` | HTTP listen adresi |
| `PUBLIC_IP` | Yok | Zorunlu, gerçekten public ve route edilebilir IPv4 |
| `UDP_PORT_MIN` | `40000` | ICE UDP alt portu |
| `UDP_PORT_MAX` | `40020` | ICE UDP üst portu |
| `ICE_LITE` | `false` | Opt-in Alexa ICE-Lite uyumluluk deneyi |
| `RTC_AUDIO_PRIME_ENABLED` | `false` | Opt-in outbound Opus audio priming tanısı |
| `RTC_AUDIO_PRIME_DURATION` | `10s` | Priming süresi; pozitif 20 ms katı |
| `RTC_AUDIO_PRIME_MODE` | `silence` | Priming payload modu: `silence` veya development-time pre-encoded `tone` |
| `SESSION_API_TOKEN` | Yok | Zorunlu güçlü bearer token |
| `RECORDINGS_DIR` | `/data/recordings` | Yazılabilir OGG dizini |
| `SESSION_TTL` | `10m` | Otomatik oturum cleanup süresi |
| `LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

Servis başlangıçta public IPv4'ü, port sırasını, token'ı, TTL'yi ve kayıt dizininin oluşturulup yazılabilir olmasını doğrular. `.env.example` içindeki `203.0.113.10` RFC 5737 dokümantasyon adresidir ve servis tarafından bilerek reddedilir; gerçek VPS IPv4'ünüzle değiştirin.

`ICE_LITE` varsayılan olarak kesinlikle kapalıdır ve normal full-ICE davranışı korunur. Yalnız Alexa cihaz/firmware birlikte çalışabilirliğini denemek için deployment `.env` dosyasında açıkça `ICE_LITE=true` ayarlayın. Bu seçenek genel SDP veya ağ davranışı önerisi değildir.

Outbound audio priming de varsayılan olarak kapalıdır. `RTC_AUDIO_PRIME_ENABLED=true` yalnız Alexa medya yolu tanısı için mevcut negotiated Opus track üzerinden paket gönderir; `silence` mevcut sessiz payload davranışını, `tone` ise runtime encoder gerektirmeyen gömülü 440 Hz Opus test tonunu kullanır.

Token üretin:

```bash
openssl rand -hex 32
```

## VPS kurulumu

Firewall'u açın:

```bash
sudo ufw allow 8080/tcp
sudo ufw allow 40000:40020/udp
```

AWS Security Group, Hetzner Firewall, DigitalOcean Cloud Firewall veya başka bir cloud-provider firewall varsa aynı TCP ve UDP portlarını orada da açın.

Yapılandırıp başlatın:

```bash
cp .env.example .env
# .env içindeki PUBLIC_IP ve SESSION_API_TOKEN değerlerini değiştirin
# Alexa ICE-Lite deneyi gerekiyorsa ayrıca ICE_LITE=true ayarlayın
docker compose up -d --build
curl http://VPS_PUBLIC_IP:8080/healthz
```

Beklenen health yanıtı:

```json
{"status":"ok"}
```

Coolify'da repository'yi Custom Docker Compose olarak deploy edebilirsiniz. Domain veya reverse proxy gerekmez; servis public IPv4 üzerindeki 8080/TCP portundan erişilir. Coolify port mapping'lerini değiştirmeyin.

Compose, uygulama başlamadan önce bind-mounted `recordings/` dizininin ownership'ini UID/GID 10001 olarak hazırlayan tek amaçlı `recordings-init` servisini çalıştırır. Ana `home-brain-rtc` container'ı hiçbir zaman root olarak çalışmaz.

Linux host'ta bind-mounted `recordings/` yazılamıyorsa container'ı root çalıştırmayın. Host dizininin UID/GID 10001 tarafından yazılabilir olmasını sağlayın:

```bash
sudo chown -R 10001:10001 recordings
```

## Lambda kurulumu

AWS Lambda runtime olarak Node.js 24 seçin. Environment variables:

```text
RTC_SERVER_URL=http://VPS_PUBLIC_IP:8080
RTC_SERVER_TOKEN=<SESSION_API_TOKEN ile aynı değer>
```

Lambda'ya yapıştırılacak ana dosya: [`lambda/index.mjs`](lambda/index.mjs). Handler adı `index.handler` olmalıdır.

AWS Console'a ZIP yüklemek için:

```bash
cd lambda
zip function.zip index.mjs
```

Harici npm bağımlılığı yoktur. `package.json` yalnız ESM/Node sürümü ve test komutunu tanımlar.

Lambda şu directive'leri destekler:

- `Alexa.Authorization.AcceptGrant`
- `Alexa.Discovery.Discover`
- `Alexa.ReportState`
- `Alexa.RTCSessionController.InitiateSessionWithOffer`
- `Alexa.RTCSessionController.SessionConnected`
- `Alexa.RTCSessionController.SessionDisconnected`

Discovery endpoint'i `home-brain-001`, friendly name `Home Brain`, category `CAMERA`, RTCSessionController v3 ve `isFullDuplexAudioSupported: true` olarak döner. EndpointHealth retrievable `true`, proactivelyReported `false` değerlerini kullanır.

## Alexa testi

Alexa Developer Console'da skill'inizi açın ve WebRTC debugger'ı etkinleştirin. Lambda ile skill endpoint bağlantısını tamamladıktan sonra desteklenen Echo cihazına söyleyin:

```text
Alexa, talk to Home Brain
```

Önce Lambda loglarında directive metadata'sını, sonra container loglarında `session_created`, `sdp_answer_generated`, ICE/PeerConnection state ve `recording_started` event'lerini kontrol edin. Loglar token veya tam SDP içermez.

## Operasyon

Container logları:

```bash
docker compose logs -f
```

Kayıtlar:

```bash
ls -lah recordings/
```

Kayıt oynatma:

```bash
ffplay recordings/<file>.ogg
```

Alternatif olarak `.ogg` dosyasını VLC ile açın.

## Geliştirme ve doğrulama

Gereken sürümler: Go 1.26+, Node.js 24+, Docker ve Docker Compose v2.

```bash
go test ./...
go test -race ./...
node --test lambda/index.test.mjs
docker build .
cp .env.example .env
docker compose config
```

`.env` gerçek değerler içerdiğinden Git tarafından ignore edilir.

## Önemli Pion ayarları

- `SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})`
- `SetEphemeralUDPPortRange(UDP_PORT_MIN, UDP_PORT_MAX)`
- `SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)`
- Yalnız `ICE_LITE=true` ise `SetLite(true)`; varsayılan full ICE'dır
- `SetICEAddressRewriteRules` ile host candidate'ı `PUBLIC_IP` değerine `ICEAddressRewriteReplace`
- Deprecated `SetNAT1To1IPs` kullanılmaz
- `GatheringCompletePromise` ile non-trickle complete answer
- Yalnız Opus audio codec kaydı
- Full-duplex negotiation için local Opus track ve RTCP reader

## Sorun giderme

### Lambda timeout / ENDPOINT_UNREACHABLE

Go endpoint'i answer ve ICE gathering için 4,5 saniye, Lambda HTTP çağrısı 4,8 saniye sınırı kullanır. VPS erişimini, 8080/TCP firewall'u, container health durumunu ve Lambda'nın internet egress'ini kontrol edin.

### Answer içinde candidate yok

`PUBLIC_IP` gerçek public IPv4 olmalı. Container loglarında config startup hatası arayın. `GatheringCompletePromise` tamamlanmadan answer dönmez; timeout varsa UDP bind ve host network durumunu kontrol edin.

### UDP firewall kapalı

Hem UFW hem cloud-provider firewall üzerinde 40000–40020/UDP inbound açık olmalıdır. TCP 8080'in açık olması medya için yeterli değildir.

### Candidate içinde Docker private IP görünüyor

Doğru image/version çalıştığını ve `PUBLIC_IP` değerinin container'a geçtiğini kontrol edin. Kod `SetICEAddressRewriteRules` kullanır; `SetNAT1To1IPs` veya manuel SDP string replacement kullanılmaz.

### PUBLIC_IP yanlış

Private, loopback, CGNAT, link-local, documentation ve IPv6 adresleri startup'ta reddedilir. VPS sağlayıcısının doğrudan route edilen public IPv4'ünü kullanın.

### Opus track gelmiyor

Offer'da `opus/48000` bulunmalıdır. WebRTC debugger'da audio m-line ve answer direction'ı kontrol edin. Video m-line reddedilebilir; bu audio'yu reddetmemelidir. `SessionConnected` gelmemesi kayıt başlangıcını engellemez; kayıt ilk Opus RTP ile `OnTrack` üzerinden başlar.

### Session dosyası finalize edilmiyor

Alexa `SessionDisconnected`, API DELETE, TTL, PeerConnection failed/closed veya uzun disconnected durumunun geldiğini loglardan kontrol edin. `disconnected` beş saniye grace süresi kullanır. Container'a SIGTERM verildiğinde graceful shutdown bütün writer'ları kapatır; `kill -9` OGG finalization garantisi vermez.

### Kayıt dizini permission hatası

Container UID/GID 10001 ile çalışır. Bind mount dizinini bu kullanıcı için yazılabilir yapın. Servis startup probe başarısızsa başlamaz.

## Bilinen riskler

- Otomatik testler iki yerel Pion peer ile protokolü doğrular; gerçek Alexa firmware/Echo uyumluluğu fiziksel cihaz ve Developer Console ile doğrulanmalıdır.
- TURN kapsam dışıdır. Bazı NAT/topolojiler direct public host candidate ile bağlantı kuramayabilir.
- Audio-only interkom Amazon tarafından desteklense de mixed video/audio offer davranışı cihaz/firmware bazında gerçek cihaz testi gerektirir.
- Düz HTTP yalnız private MVP içindir.
