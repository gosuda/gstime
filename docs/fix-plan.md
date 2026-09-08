# 코드리뷰 수정 계획

상태: F01–F23, L01 구현 및 로컬 검증 완료. 추가 요청한 무할당 UTC API도 적용했다. 기준: 2026-09-08 전체 코드리뷰.

## 범위와 완료 정의

리뷰의 P1 9개, P2 14개를 F01–F23으로, 린트 실패를 L01로 추적한다. 아래 24개 항목을 구현했으며 회귀·integration·allocation 검증 결과는 마지막 절에 기록한다.

완료는 각 재현이 수정 전 실패·수정 후 성공하고, 실제 API/loopback 경로 및 통합 검증을 통과한 상태다. 기존 suite가 통과한다는 이유로 개별 재현을 생략하지 않는다. 리뷰에서 사용한 임시 프로그램은 제거했으므로 시나리오를 집중 회귀 테스트로 복원한다.

범위 밖: 외부 의존성 도입, 암호 알고리즘 교체, 신규 persistence/telemetry/retry 프레임워크, unrelated 리팩터링. 실제 VM 복원·suspend와 Linux syscall 실패 검증은 별도 환경 없이는 완료했다고 주장하지 않는다.

## 공통 설계 결정

1. **Hard bound와 통계 제어 분리.** `MeasurementInput`의 raw scale/read-error 입력을 명시하고 hard interval을 qualified SI 모델로 계산한다. 추정 RTT·회귀값은 hard bound를 축소하거나 교체하지 못한다. 각 오차의 좌표·소유자·반올림 방향을 명시해 누락과 이중 계산을 방지한다.
2. **기존 stamp 모델 재사용.** `ntp.Stamp`/`RequestSlot`에 송수신 mapping provenance를 보존한다. persistent StepGeneration으로 교환 사이의 모든 step을 검출한다. 초기 anchor 이후 재초기화는 step으로 분류한다. 최근 256개 mapping history 밖의 교환은 거부하고, history 순회는 취득 경로에만 둔다.
3. **읽기 경로 제약 유지.** ContinuityGuard 수정에 새 mutex를 넣어 완료 순서를 직렬화하는 해법은 채택하지 않는다. 중첩 읽기와 실제 인과적으로 뒤따르는 역행을 구분하는 순서 프로토콜을 먼저 검증한다. `Now()`의 primary raw read 1회, 무할당, 비차단 요구를 유지한다.
4. **Watermark 단일 의미.** 유효한 lower만 원자적으로 누적하고 assurance/config/leap identity와 묶는다. 오래된 snapshot의 값으로 덮어쓰지 않는다. expiry·DESYNC·identity 변경 이후 역사적 watermark가 현재 인증으로 쓰이지 않도록 한다.
5. **설정 API는 우선 유지.** `NewSyncEngine(cfg, svc, ...)`에서 canonical ID와 서비스 정책을 일치시킨다. 별도의 두 설정을 조용히 허용하지 않는다. 설정 입력의 slice를 복사하며, 오류를 반환할 수 있는 기존 constructor를 활용한다.
6. **정밀한 canonical identity.** Config의 일반 입력 타입은 유지하되, identity용 canonical 문서에서는 int64/uint64 ns 값을 항상 십진 문자열로 표현한다. 작은 값도 동일한 표현을 써 값 크기에 따라 schema가 바뀌지 않게 한다. 나머지 값은 RFC8785 규칙으로 직렬화한다. 일반 Config JSON과 canonical identity 문서가 다른 계약임을 문서화한다.
7. **제어는 연속적으로 변경.** 초기 anchor와 명시적인 step을 제외한 poll은 `ReanchorContinuity`/slew를 사용한다. primary component는 control target에만 적용하고, assurance는 항상 전체 N-F hull을 유지한다. 회귀 불가 시 샘플을 임의로 신뢰하지 않고 기존 qualified rate를 유지하며, hard evidence가 없으면 holdover/desync 정책을 따른다.
8. **실패 전 상태 보존.** checked arithmetic과 검증이 성공하기 전 anchor·mapping·watermark를 변경하지 않는다. 통제 불가능한 입력은 오류로 거부하고, panic recover·고정 padding 증대·음수 bound의 0 clamp로 결함을 숨기지 않는다.

## 호환성 및 호출자 이관

- `ConfigID` canonical 표현 변경은 identity-breaking 변경이다. 기본 설정을 포함해 ID를 재계산하고, 과거 ID로 바인딩된 anchor와 CommitWait를 재사용하지 않는다. 다른 노드와 연동하면 동일 표현으로 조율해 배포한다. 구형 digest를 허용하는 호환 alias는 추가하지 않는다.
- 잘못된 endpoint/domain 조합과 서비스/엔진 설정 불일치는 이제 startup error다. 테스트에서 사용하던 임의의 zero ConfigID도 실제 fixture 설정에서 계산하도록 이관한다.
- `MeasurementInput`의 새 필수 수치와 stamp provenance를 생산자·테스트·custom transport 예제까지 이관한다. 통계 API에 오류 반환이 필요하면 모든 호출자에서 처리한다. exported symbol을 바꾸기 전 gopls references로 범위를 확정한다.
- Gregorian/leap 입력 중 기존에 잘못 허용하던 범위 밖·삭제된 second·불가능한 transition은 오류가 된다. 유효한 GSTL1 encoding과 history ID는 보존한다.
- README와 기존 verification 문서는 관련 동작이 smoke test로 증명된 후 갱신한다. SPEC을 구현에 맞춰 완화하거나 SPEC.md의 hash/timestamp 증거를 다시 쓰지 않는다.

## 작업 목록

각 항목을 집중 회귀와 함께 독립적인 변경으로 작성한다. 같은 원인의 밀접한 항목만 같은 commit/PR에 묶는다. 단계는 권장 우선순위이며, 정확한 선행 조건은 각 항목에 적는다.
### 입력 경계 차단

- [x] **F01 — NTS nonce 길이 검증** (P1)
  - 파일: `nts/packet.go`
  - 수정: 지원 AEAD의 nonce/tag 제약을 Open 전에 검증한다. 라이브러리 panic을 recover로 숨기지 않는다.
  - 완료 증거: AEAD15/30의 잘못된 nonce가 panic 없이 오류로 반환되고 정상 벡터와 loopback NTS가 유지된다.
  - 선행: 없음.

- [x] **F05 — NTP 요청 origin binding** (P1)
  - 파일: `sync.go`, `ntp/slot.go`
  - 수정: 요청마다 binding 값을 생성하고 응답 origin을 측정·KoD 채택 전에 대조한다. NTS에서도 기존 UID 인증과 함께 적용한다.
  - 완료 증거: 다른 origin과 이전 요청 응답은 거부하고 정확히 일치하는 NTP/NTS 응답은 성공한다.
  - 선행: 없음.

### 시간 인증 핵심

- [x] **F06 — 인증 epsilon overflow** (P1)
  - 파일: `core/fixedpoint.go`, `clock/public.go`, `service.go`
  - 수정: 정수 기반 unsigned 절대거리와 checked/saturating 합산으로 두 epsilon 경로를 통일한다. 무한 오차를 유효한 작은 반경으로 게시하지 않는다.
  - 완료 증거: 서로 반대 signed 극값에서 tiny epsilon/SYNCED가 사라지고 public cap 경계와 정상값의 outward containment가 유지된다.
  - 선행: 없음.

- [x] **F14 — Estimate mapping checked arithmetic** (P2)
  - 파일: `core/fixedpoint.go`, `clock/estimate.go`, `service.go`
  - 수정: Evaluate/Step/reanchor의 anchor·slew·debt 합을 검사하고 실패 전 state를 변경하지 않는다. 필요한 반환 계약과 모든 호출자를 같이 갱신한다.
  - 완료 증거: MaxInt64+1과 MinInt64-1이 ErrOverflow이며 기존 mapping은 유지된다. 정상 reanchor와 slew 부채 보존은 통과한다.
  - 선행: 없음.

- [x] **F04 — 동시 raw 읽기 연속성** (P1)
  - 파일: `clock/continuity.go`
  - 수정: 완료 순서를 획득 순서로 취급하지 않는다. raw/token/generation 상태를 일관된 순서로 갱신하고 지연된 읽기가 새 세대를 덮어쓰지 않게 한다.
  - 완료 증거: 101,102,103 획득/101,103,102 완료에서 token 유지. 실제 비중첩 역행·token/gen 변경은 검출. Now 경로에 새 mutex·allocation 없음.
  - 성능 조건: moving backend와 workers=1/4/8에서 유효 결과 비율을 함께 측정한다. DESYNC shortcut의 처리량을 정상 baseline으로 사용하지 않는다.
  - 선행: 없음.

- [x] **F02 — 획득 구간의 hard rate bound** (P1)
  - 파일: `ntp/measurement.go`, `sync.go`
  - 수정: HardInterval을 qualified raw scale과 송수신 raw 좌표에서 유도한다. estimate RTT는 통계용으로 분리하고 반올림과 오류 소유권을 명시한다.
  - 완료 증거: 1초·200ppm·비대칭 지연 재현에서 truth를 포함한다. 독립 유리수 모델로 양쪽 scale 극값과 turnaround 경계를 확인한다.
  - 선행: F04, F14.

- [x] **F03 — Mapping step 교환 폐기** (P1)
  - 파일: `ntp/slot.go`, `clock/estimate.go`, `sync.go`
  - 수정: 기존 Stamp/RequestSlot에 mapping provenance를 보존한다. step과 raw discontinuity는 폐기하며 연속 reanchor는 필요한 mapping history로 검증한다.
  - 완료 증거: 200ms estimate step 교환이 거부된다. 정상 연속 rate/slew 변경과 public-only 변경은 계약대로 처리된다.
  - 선행: F01, F05, F04, F14.

- [x] **F07 — 초기 watermark의 전체 signed 범위** (P1)
  - 파일: `assurance/assurance.go`
  - 수정: 최초 유효 interval 이전에는 watermark를 미설정으로 두거나 전체 범위에 안전한 초깃값을 사용한다.
  - 완료 증거: -5e18ns 최초 interval에서 watermark가 미래 사실을 만들지 않고 future CommitWait가 즉시 성공하지 않는다.
  - 선행: 없음.

- [x] **F10 — 읽기 기반 watermark와 CommitWait 진행** (P2)
  - 파일: `service.go`, `assurance/assurance.go`, `publish/snapshot.go`
  - 수정: 유효한 전파 interval의 lower를 identity에 귀속된 원자적 watermark에 반영한다. stale publication이 이를 낮추거나 다른 identity에 적용하지 못하게 한다.
  - 완료 증거: 새 poll 없이 raw 전진만으로 CommitWait 성공. HOLDOVER에서도 진행하고 expiry·DESYNC·identity 변경에서는 실패하며 복구가 기존 wait을 자동 재개하지 않는다.
  - 성능 조건: watermark 갱신은 PastWatermark/CommitWait에 한정하고 불필요한 CAS를 생략한다. 취소·identity·현재 유효성을 검증한 뒤 이미 충족한 wait는 timer 생성 없이 반환한다.
  - 선행: F04, F07.

### 설정 identity 일치

- [x] **F09 — Endpoint/domain 중복 할당 거부** (P1)
  - 파일: `config/config.go`
  - 수정: 동일 endpoint가 여러 fault domain에 속하는 설정을 거부한다. 문자열 수준에서 안전한 hostname/IP/port 정규화만 사용하고 DNS로 독립성을 추측하지 않는다.
  - 완료 증거: 동일 endpoint를 a/b/c에 할당한 설정은 시작 단계에서 실패한다. 한 domain의 여러 endpoint는 계속 허용한다.
  - 선행: 없음.

- [x] **F19 — 정수 보존 ConfigID** (P2)
  - 파일: `config/config.go`
  - 수정: identity용 canonical 표현에서 큰 정수 ns 필드를 십진 문자열로 정의하고 float64 round-trip을 제거한다. 일반 Config 입력 타입과 identity 직렬화 계약을 구분한다.
  - 완료 증거: MaxAgeNs=2^53와 2^53+1의 canonical bytes/ConfigID가 다르고 int64/uint64 경계가 보존된다.
  - 선행: 없음.

- [x] **F20 — RFC8785 scalar 직렬화** (P2)
  - 파일: `config/config.go`
  - 수정: 문자열·음의 0·number formatting·정렬을 JCS 규칙에 맞춘다. 외부 라이브러리 없이 기존 config 경계에서 구현한다.
  - 완료 증거: R&D가 HTML escaping 없이 직렬화되고 -0은 0이 된다. 독립 RFC fixture의 전체 canonical bytes와 digest가 일치한다.
  - 선행: 없음.

- [x] **F08 — Effective configuration과 서비스 결합** (P1)
  - 파일: `sync.go`, `service.go`, `config/config.go`
  - 수정: NewSyncEngine에서 계산한 ConfigID와 서비스 identity를 검증하고 공유 정책의 불일치를 거부한다. Sources를 포함한 설정은 복사해 생성 후 외부 변경을 차단한다.
  - 완료 증거: F=1/min3 서비스에 F=0/min1 엔진 연결은 네트워크 I/O 전에 실패한다. 생성 후 원본 Sources 변경이 엔진 정책을 바꾸지 않는다.
  - 선행: F09, F19, F20.

### 추정·제어·presentation

- [x] **F22 — 비유한 회귀 입력과 결과 거부** (P2)
  - 파일: `source/estimate.go`
  - 수정: phase/distance/weight와 회귀 중간값·결과의 NaN/Inf 및 invalid denominator를 오류 또는 unavailable로 처리한다.
  - 완료 증거: NaN phase 및 Inf distance에서 성공 결과를 반환하지 않는다. 유한한 정상·singular 경계 fixture는 계약대로 동작한다.
  - 선행: 없음.

- [x] **F21 — Runs pruning suffix 영구 반영** (P2)
  - 파일: `source/estimate.go`
  - 수정: Fit이 채택한 suffix를 track의 retained history에 반영하고 추가 샘플에도 제거 상태를 보존한다.
  - 완료 증거: 20개에서 11개를 채택한 뒤 10개 추가 시 최대21개만 유지하며 폐기한 이전 regime이 부활하지 않는다.
  - 선행: 없음.

- [x] **F12 — 실제 EstimateTrack과 연속 제어 연결** (P2)
  - 파일: `sync.go`, `service.go`, `source/estimate.go`, `source/assurance.go`, `clock/estimate.go`
  - 수정: endpoint별 통계 이력을 실제 polling 경로에 연결한다. 이전 target과 statistical target으로 primary/control target을 선택하고, 첫 anchor 이후에는 continuity-preserving reanchor/slew로 제어한다.
  - 완료 증거: 분리된 components 사이 gap을 target으로 선택하지 않는다. 정상 poll 전후 같은 raw에서 E_old=E_new. planted phase/rate 추정과 제어가 작동하고 통계적 제거가 full assurance hull을 축소하지 않는다.
  - 성능 조건: 회귀와 mapping history 순회는 writer에 둔다. idle뿐 아니라 active slew/smear 및 실제 publication을 포함한 무할당·tail-latency gate를 통과한다.
  - 선행: F02, F03, F08, F14, F21, F22.

- [x] **F13 — 완료된 smear bias 보존** (P2)
  - 파일: `clock/public.go`
  - 수정: segment가 없는 완료 replan도 terminal bias를 유지하도록 producer/consumer 의미를 일치시킨다.
  - 완료 증거: bias가 이미 +1초 또는 -1초인 zero-rate replan에서 즉시값·장기값이 유지되고 jump/stall이 생기지 않는다.
  - 선행: 없음.

### Civil UTC

- [x] **F17 — 불가능한 leap transition 거부** (P2)
  - 파일: `core/leap.go`
  - 수정: NewLeapHistory와 ParseLeapHistory가 동일한 civil transition 검증을 사용하도록 한다.
  - 완료 증거: transition=1처럼 자정이 아닌 history는 생성·파싱 모두 실패하며 정상 양/음의 윤초 history는 왕복한다.
  - 선행: 없음.

- [x] **F15 — UTC 변환 범위 검사** (P2)
  - 파일: `core/leap.go`
  - 수정: 일반·양의 윤초 경로 모두 SI seconds와 nanoseconds 합성을 checked arithmetic으로 수행한다.
  - 완료 증거: 2500년 입력은 ErrOverflow. 표현 가능한 signed 양끝과 정상 날짜는 정확히 왕복한다.
  - 선행: 없음.

- [x] **F16 — 음의 윤초의 삭제된 civil second** (P2)
  - 파일: `core/leap.go`
  - 수정: UTC label 변환에서도 해당 history가 삭제한 second를 거부한다.
  - 완료 증거: 음의 윤초 전날 23:59:59는 실패하고 마지막 유효 second 및 다음날 00:00:00은 서로 다른 정확한 instant로 변환된다.
  - 선행: 없음.

- [x] **F18 — 윤초 문자열 표현** (P2)
  - 파일: `core/leap.go`
  - 수정: SecondOfDay=86400을 23:59:60으로 출력하고 일반 civil formatting은 유지한다.
  - 완료 증거: 정상 양의 윤초 label이 YYYY-MM-DDT23:59:60.nnnnnnnnnZ로 출력된다.
  - 선행: 없음.

### 수명주기·예제·품질

- [x] **F11 — 수동 PollOnce 종료 수명주기** (P2)
  - 파일: `sync.go`
  - 수정: 수동/background poll을 같은 취소·작업 등록 수명주기로 관리한다. Close가 새 작업을 막고 진행 중 query를 취소·await하도록 한다.
  - 완료 증거: channel barrier로 query 실행 중 Close를 호출하면 query 종료 후 반환한다. concurrent Start/PollOnce/Close와 반복 Close에 race·deadlock 없음.
  - 선행: 없음.

- [x] **F23 — 예제의 가짜 assurance 게시 제거** (P2)
  - 파일: `examples/main.go`, `example_test.go`, `README.md`
  - 수정: 실사용 예제는 실제 성공 round와 명시적으로 제공된 leap history를 기다리게 한다. unsmeared source 계약을 지키고 실패 시 인증값을 만들지 않는다. simulation은 별도 명시된 test/example 경계에 둔다.
  - 완료 증거: 로컬 정상 upstream에서 성공 round 이후에만 assured 출력. upstream 실패에서는 오류/미동기화 상태이며 wall time 기반 PublishAssuranceRound가 실행되지 않는다.
  - 선행: F08, F11, F12, F15, F16, F17, F18.

- [x] **L01 — 현재 gojgp 오류 해결** (필수 품질 게이트)
  - 파일: `core/`, `assurance/`, `ntp/`, `nts/`, `publish/`, `service.go`, `sync.go`, `service_lifecycle_test.go`
  - 수정: 관찰된 상수 errors.New 선언을 package scope로 이동한다. 공유 sentinel을 유지하며 errors.Is 계약을 보존하고 lint suppression은 추가하지 않는다.
  - 완료 증거: gojgp check의 unit test와 AST lint 단계가 모두 통과한다.
  - 선행: 없음.

## 실행 순서와 통합 경계

권장 순서:

```text
F01 → F05 → F06 → F14 → F04 → F02 → F03 → F07 → F10
→ F09 → F19 → F20 → F08 → F22 → F21 → F12 → F13
→ F17 → F15 → F16 → F18 → F11 → F23 → L01
```

이 순서는 우선순위와 같은 파일의 편집 충돌을 줄이기 위한 기본 실행 순서이지, 모든 작업의 의미상 의존성을 뜻하지 않는다. 독립적인 protocol, numeric, config, UTC, source-statistics 작업은 분리해 수행할 수 있다. `sync.go`와 `service.go`의 합치는 변경은 통합 담당자 한 명이 순서대로 적용한다. 인터페이스 변경을 다른 작업에 넘길 때에는 필드 타입·단위·오류·소유권을 먼저 확정한다.

권장 commit/PR 경계: protocol 입력 검증 / checked numeric / continuity guard / acquisition proof / watermark / configuration identity / estimator 제어 / UTC / smear / engine lifecycle / 실행 예제 / static-error lint. 파일 이동·일괄 포맷 변경은 섞지 않는다.

## 검증 계획

### 항목별

- 각 수정 전에 리뷰 재현을 최소한의 실패 회귀로 복원한다. source-text나 field forwarding이 아니라 반환값·상태·containment·종료 동작을 검증한다.
- 네트워크는 loopback UDP/TLS만 사용하고, 시간은 제어된 RawClock과 독립 true-SI 모델로 정의한다. 서버 처리시간·양방향 비대칭·scale 하한/상한·read uncertainty를 구별한다.
- 동시성은 channel barrier 또는 `testing/synctest`로 순서를 고정한다. 고정 sleep, 재시도로 flaky failure를 숨기는 방식은 사용하지 않는다.
- NTS는 nonce/ciphertext framing fuzz를 추가하되 실제 등록 AEAD를 사용한다. nonce 길이 때문에 panic하는 경로와 정상 RFC 벡터를 함께 검증한다.
- 수치·canonical fixture의 기대값을 production helper로 계산하지 않는다. `math/big` 유리수·정수 reference 또는 RFC의 독립 fixture를 테스트에서 사용한다.
- P1 항목을 먼저 닫되 P2를 미완료 상태로 남겨 전체 작업을 완료했다고 표시하지 않는다.

### 통합 게이트

모든 구현이 합쳐진 뒤 다음을 실행한다. 구현 단계 중에는 바뀐 경로의 좁은 재현을 먼저 실행한다.

```sh
go fmt ./...
gojgp check
go vet ./...
go vet -tags integration ./...
go test -race -count=1 ./...
go test -race -tags integration -count=1 ./...
go test -race -shuffle=on -count=1 ./...
go test -race -tags integration -shuffle=on -count=1 ./...
go test ./nts -run '^$' -fuzz '^FuzzReadServerResponse$' -fuzztime=5s -parallel=2
go test -run '^$' -bench 'Benchmark(Now|NowParallel|NowPublicAssured|NowPublicAssuredParallel)$' -benchmem -count=3 .
```

새 NTS response fuzz target도 동일한 bounded smoke 방식으로 실행한다. 네트워크 성공·실패 양쪽에서 실제 예제 실행을 관찰한다. `Now`/`NowPublicAssured`의 0 B/op·0 allocs/op과 동시 읽기 성능을 기준과 비교한다. 평균 benchmark 수치만으로 SPEC의 p99 요구를 충족했다고 주장하지 않으며, 목표 플랫폼별 latency 분포 관측을 별도 기록한다.

Linux와 macOS CI에서 race 및 integration 결과를 확인한다. 지원되는 architecture별 수치 fixture 결과를 확인하고, 실행하지 못한 조합은 미검증으로 남긴다. CI의 reachable vulnerability scan도 release gate에 포함하되 데이터베이스 시점의 한계를 명시한다.

## 시작 기준과 출시 조건

리뷰에서 관찰한 기존 기준: 두 race suite와 두 vet profile은 통과, gofmt는 clean, NTS-KE fuzz는 통과. `gojgp check`는 static errors.New 린트에서 실패했다. 이는 이전 리뷰 결과이며 이 계획 작성 과정에서 다시 실행한 결과가 아니다.

출시 조건은 F01–F23 및 L01의 완료 증거 확보, 실제 취득 경로의 truth containment, 정확한 CommitWait 진행·중단, 정상 동시 읽기의 연속성, canonical identity 이관, 예제의 인증 정직성, 최종 품질 게이트 통과다. 어떤 기준도 기존 테스트 통과나 문서 변경만으로 대체하지 않는다.


## 구현 전 Hot path 성능 조사 — 2026-09-08

이 절은 수정 전 baseline과 당시 격리된 후보 연산의 결과다. 적용 후 결과는 마지막 절과 비교한다. 당시 코드·원시 출력·집계값은 세션 아카이브 `local://gstime-hotpath-evidence.zip`에 보관했다.

### 측정 방법과 유효성

- Go 1.27.0, darwin/arm64, Apple M1: performance core 4개와 efficiency core 4개. core affinity는 고정하지 않았다.
- 성능 측정은 race instrumentation 없이 순차 실행했다. 별도 벤치마크 작업끼리 동시에 부하를 주지 않았다.
- 기존 benchmark: 250ms, 3회, GOMAXPROCS=1/4/8. 추가 status-aware benchmark: 200ms, 3회, worker=GOMAXPROCS. 산술/primitive는 단일 worker 5회.
- `RunParallel`의 ns/op는 전체 경과 시간/총 호출 수인 처리량 역수다. 개별 요청 지연이나 p99가 아니다.
- tail latency: worker당 10,000회 warmup 뒤 2,000,000회 호출 중 64번째마다 측정, 3회 반복. timer 비용을 포함하며 percentile끼리 빼지 않았다. timer-only p99 중앙값은 42ns다.
- Frozen은 고정 raw 값·무잠금 테스트 backend로 engine 계산/atomic 경합을 분리한다. Simulated는 저장소의 mutex 기반 backend이며 시간은 전진시키지 않는다. System은 실제 단조 시간이 전진하는 `SystemRawClock`이다.
- 매 호출의 SYNCED/interval 유효성을 worker-local counter로 기록했다. 공유 counter의 per-call atomic 비용은 넣지 않았다. aggregate는 worker 종료 시 수행했다.

### 현재 정상 경로의 기준

기존 `service_bench_test.go`의 단일 호출, 3회 중앙값:

| API | ns/op | B/op | allocs/op |
|---|---:|---:|---:|
| Now | 44.03 | 0 | 0 |
| NowPublicAssured | 41.58 | 0 | 0 |
| NowUnixProjection | 97.16 | 0 | 0 |
| NowUtc | 150.10 | 48 | 1 |

이는 고정 Simulated fixture이며 active slew/smear와 raw 진행에 따른 guard CAS를 대표하지 않는다. NowUtc의 pointer-valued estimate 할당은 기존 API 비용이고, core Now의 무할당 보장과 구분한다.

status-aware `RunParallel` 처리량, 3회 중앙값:

| Backend/API | 1 worker ns/op | 4 workers ns/op | 8 workers ns/op | 유효 결과 비율 |
|---|---:|---:|---:|---|
| Frozen / Now | 31.49 | 8.34 | 6.46 | 모두 100% |
| Frozen / Public | 31.38 | 37.28 | 49.97 | 모두 100% |
| Frozen / PastWatermark | 22.96 | 6.10 | 4.61 | 모두 100% |
| Simulated / Now | 42.76 | 157.40 | 192.70 | 모두 100% |
| Simulated / Public | 41.18 | 155.60 | 202.10 | 모두 100% |
| Simulated / PastWatermark | 31.88 | 158.80 | 192.30 | 모두 100% |
| System / Now | 50.33 | 34.95 | 54.72 | 100% / 7.282% / 0.06426% |
| System / Public | 50.25 | 88.70 | 148.40 | 100% / 9.434% / 0.1348% |
| System / PastWatermark | 41.82 | 34.57 | 52.19 | 100% / 5.067% / 0.04531% |

System 병렬 수치는 정상 경로 기준으로 사용하지 않는다. F04의 false discontinuity 이후 DESYNC 반환 경로가 섞이기 때문이다. 수정 후 더 많은 유효 계산을 수행해 ns/op가 증가해도, 현재 오류 경로보다 느리다는 이유로 correctness fix를 거부하면 안 된다.

### Tail latency와 writer 영향

샘플링 실행의 p99 중앙값, 단위 µs:

| Backend/API | 1 worker | 4 workers | 8 workers | 상태 |
|---|---:|---:|---:|---|
| Frozen / Now | 0.084 | 0.084 | 0.167 | 모두 유효 |
| Frozen / Public | 0.084 | 0.209 | 0.834 | 모두 유효 |
| Simulated / Now | 0.084 | 6.333 | 32.458 | 모두 유효 |
| Simulated / Public | 0.084 | 8.458 | 32.958 | 모두 유효 |
| System / Now | 0.084 | — | — | 병렬은 오류 경로 혼합 |
| System / Public | 0.084 | — | — | 병렬은 오류 경로 혼합 |
| Frozen + writer / Now | 0.083 | 0.084 | 0.167 | 모두 유효 |
| Frozen + writer / Public | 0.084 | 0.209 | 0.750 | 모두 유효 |

샘플링 실행의 System 8-worker는 warmup 후 Now/Public 모두 유효 결과 0%였다. 따라서 낮은 오류 경로 p99는 성능 gate 통과로 인정하지 않는다.

writer는 2ms ticker로 현재 `PublishAssuranceRound`를 호출했다. 실제 완료 횟수는 실행당 2–47회였으며 scheduler 포화 때문에 500Hz를 달성했다고 주장하지 않는다. 관측 범위에서 유효성이 유지되고 큰 p99 증가는 없었지만, 향후 추가될 회귀 계산·history writer의 부하까지 검증한 것은 아니다. 모든 latency 결과는 closed-loop 합성 부하이며 production SLO나 open-loop 포화 부하 증명이 아니다.

### 병목 분리 결과

- Simulated/Now 8-worker mutex profile: 기록된 누적 mutex delay의 97.80%가 backend `Read`와 `ScaleEnvelope`를 지나는 경로였다. core의 무잠금 publication이 느린 것이 아니라 fixture의 두 mutex 진입이 병렬 실행을 지배한다.
- Frozen/Public 8-worker CPU profile: `CompareAndSwap` flat 56.86%, `ReadPublicInstant` cumulative 70.87%. 현재 clamp는 값이 그대로여도 CAS를 수행한다. 새 watermark에 같은 불필요한 RMW 패턴을 추가하지 않는다.
- 즉시 성공하는 `CommitWait`: Frozen backend 단일 worker 중앙값 163.4ns/op, 248B/op, 3 allocs/op. allocation profile의 99.94%가 `time.NewTimer` 경로였다. 실제 대기 여부를 알기 전에 timer를 만든다.
- Frozen backend의 After/Before는 각각 31.89/32.31ns/op, 0 allocation으로 Now wrapper 수준이었다.

### 수정 후보의 분리 측정

정상 수치 입력의 단일 worker 중앙값이며 full API 수정 효과를 합산한 예측치가 아니다. 후보 정수 epsilon/checked-add는 signed 경계와 seeded 입력 10,000개를 독립 `math/big` oracle로 검증했다.

| 비교 | 현재/비검사 ns/op | 후보/활성 ns/op | 해석 |
|---|---:|---:|---|
| Float epsilon → unsigned 정수 거리+checked 합 | 4.045 | 2.384 | F06이 반드시 느려지는 것은 아님 |
| 두 덧셈 → checked 두 덧셈 | 2.239 | 2.371 | F14의 정상 입력 추가 비용은 이 측정에서 작음 |
| Estimate idle → active slew | 5.746 | 9.868 | F12 연결 후 실제 사용될 분기를 gate에 포함 |
| Frozen Public idle → active smear | 31.38 | 33.12 | 해당 활성 plan에서 무할당 유지 |

공유 scalar 갱신 primitive의 처리량 역수:

| 패턴 | 1 worker ns/op | 8 workers ns/op |
|---|---:|---:|
| Atomic load | 0.955 | 0.204 |
| 값이 같아도 CAS | 6.961 | 40.790 |
| stale candidate: load 후 반환 | 1.587 | 0.312 |
| 전진 candidate: atomic max | 7.169 | 41.300 |
| Mutex 보호 갱신 | 13.540 | 89.770 |

이 값은 scalar 상태 하나의 합성 경합 비용이다. F04의 완성된 연속성 프로토콜이나 F10의 identity 검증까지 구현한 비용이 아니며, F04 전체 overhead는 아직 미측정이다.

### 계획에 반영할 성능 조건

| 항목 | Hot path 영향 | 실행 조건 |
|---|---|---|
| F04 | 높음: 공통 guard의 shared state | 중첩 읽기에서 정상 상태를 유지한 다음 성능 비교. 새 mutex와 heap allocation 금지 |
| F06, F14 | 낮을 것으로 예상: 정수 연산 | checked arithmetic 유지. 정상·overflow 경로 분리, 0 allocation |
| F07 | 최초 publication | steady-state read에 별도 작업을 추가하지 않음 |
| F10 | 높음: watermark 공유 갱신과 wait timer | 갱신은 PastWatermark/CommitWait 경로에 한정. redundant CAS 생략. 유효성/identity/취소 검증 후 이미 충족한 wait는 timer 없이 반환 |
| F12, F13 | writer + active slew/smear | peer 회귀·history 순회는 writer에 두고 read는 bounded 계산만 수행 |
| F15–F18 | civil 변환 API에 국한 | leap table 순회를 core Now에 추가하지 않음. NowUtc의 기존 allocation은 별도 API 비용으로 표시 |
| F01–F03, F05, F08–F09, F11, F19–F23, L01 | network/config/lifecycle/statistics 중심 | Now 경로에 네트워크 검증·hash 계산·peer 순회가 들어가지 않게 유지하고 writer 간접 영향만 별도 측정 |

성능 게이트:

1. **정확성 선행:** 무장애 부하에서 `SYNCED`와 interval 유효 비율 100%. DESYNC/오류 경로는 별도 결과로 분리하며 정상 경로를 대체하지 못한다.
2. **할당:** Now, NowPublicAssured, After, Before, PastWatermark는 0B/op·0 allocs/op 유지. F10 완료 후 이미 조건을 만족한 CommitWait도 무할당을 목표로 검증한다. 실제로 blocking하는 wait의 timer 할당과 구분한다.
3. **연산 위치:** raw read 1회, 새 blocking lock 없음, peer/history traversal·config hash·I/O 없음. scalar primitive 비용을 단순 합산해 full API 성능을 보증하지 않는다.
4. **부하 행렬:** workers=1/4/8, backend 분리, idle/active slew/smear, reader-only/실제 writer 포함, 상태와 실제 publication 횟수 동시 기록. Frozen backend는 산술 격리용이지 moving-backend 검증의 대체가 아니다.
5. **회귀 조사 기준:** 동일한 정상 workload의 반복 측정 오차를 넘는 median ns/op +10% 또는 p99 +20%는 profile 조사 대상으로 삼는다. 이는 제안된 경보 기준이지 이 조사에서 검증된 허용 한도가 아니며, 정확성을 희생하는 자동 rollback 기준으로 쓰지 않는다. 정상 System 병렬 기준은 F04 수정 후 새로 확립해야 한다.
6. **SPEC gate:** qualified backend의 외부 read latency를 구분해 p99 <10µs를 확인한다. Simulated의 mutex 대기 포함 32µs 결과를 core 단독 latency로 오인하지 않는다. timer 분해능·OS scheduling·Apple M1의 서로 다른 core 특성 때문에 극소 차이와 다른 플랫폼으로의 수치 외삽은 제한한다.

### 재실행

저장소 기준 benchmark:

```sh
go test -run '^$' -bench '^BenchmarkNow' -benchmem -benchtime=250ms -count=3 -cpu=1,4,8 .
```

추가 workload는 아카이브를 풀고 gstime module 디렉터리에서 실행한다. 아래 BENCH_DIR는 풀어 놓은 디렉터리다.

```sh
go test "$BENCH_DIR/hotpath_test.go" -run '^$' -bench '^BenchmarkMeasured$' -benchmem -benchtime=200ms -count=3 -cpu=1,4,8
go test "$BENCH_DIR/hotpath_test.go" -run '^$' -bench '^Benchmark(Arithmetic|Primitives)$' -benchmem -benchtime=200ms -count=5 -cpu=1
go test "$BENCH_DIR/hotpath_test.go" "$BENCH_DIR/extras_test.go" -run '^$' -bench '^Benchmark(SharedUpdate|APIExtras)$' -benchmem -benchtime=200ms -count=3 -cpu=1,4,8
go run "$BENCH_DIR/latency_sampled.go"
go test "$BENCH_DIR/hotpath_test.go" "$BENCH_DIR/candidate_validation_test.go" -run '^TestCandidateArithmetic$' -count=1
```

프로파일 실행은 timing baseline과 분리한다. CPU profile은 Simulated/Now 및 Frozen/Public 8-worker에 각각 수집했고, mutex profile은 Simulated/Now, allocation profile은 즉시 성공 CommitWait에 수집했다. 이번 조사에서는 production Go 파일이나 영구 benchmark/test를 추가·수정하지 않았다.


## 적용 결과 및 최종 검증

F01–F23 및 L01을 적용했다. 추가로 NowUtc를 `(AssuredUtcNow, error)` 값 반환 API로 전환하고 호출자·예제·benchmark를 이관했다. 기존 tuple/포인터 반환을 남겨 두는 shim은 없다. 네 개 nanosecond canonical 필드는 정확한 십진 문자열이므로 ConfigID는 재계산해야 한다. MaxAgeNs=0의 숨은 fallback은 제거했고 양수만 허용한다.

- NTS 입력 검증, NTP origin, qualified raw rate interval, step provenance, causal continuity guard, 전체 hull 유지와 실제 통계 제어가 적용됐다.
- watermark는 assurance epoch별 atomic cell이다. 새 epoch에는 이전 숫자도 이월하지 않으며, retired reader가 새 cell을 갱신할 수 없다. 검증 중 이전 값 10000이 새 증거 1000에 남는 재현을 추가했고, 수정 전 실패·수정 후 성공했다.
- raw reset 뒤의 첫 정상 네트워크 round도 검증했다. 이전 mapping history 범위 오류로 복구하지 못하던 재현은 실패를 확인한 뒤 수정했다. raw domain이 바뀌면 과거 estimate를 읽기 API에 노출하지 않고, 취득은 bootstrap stamp로 재개한 뒤 새 hard evidence로 estimate를 다시 anchor한다.
- 수동 PollOnce도 Close 취소/await에 포함한다. 현재 유효성·identity·취소를 검증해 이미 완료한 CommitWait는 timer를 생성하지 않는다.
- 두 race suite 및 integration, 두 vet profile, gojgp 전체 게이트가 통과했다. 최종 race 검증은 shuffle을 켜서 다시 수행했다.
- NTS-KE fuzz smoke: 7,083회 실행, 통과. 새 NTS response fuzz smoke: 341,731회 실행, 통과. 실행 횟수는 이번 bounded run의 관찰값이며 coverage 완전성 지표가 아니다.
- 실제 `go run ./examples`를 loopback Config/GSTL1 fixture로 실행했다. 정상 peer는 2025-01-02 UTC와 SYNCED를 출력했고, RATE 응답 peer는 exit1 및 원래 오류를 출력하며 인증값을 만들지 않았다.

### 적용 전후 allocation과 처리량

같은 M1/Go1.27 환경, 동일한 baseline workload의 3회 중앙값. ns/op는 처리량 지표이며 개별 요청 p99와 구분한다.

| API/workload | 이전 ns/op | 적용 후 ns/op | 이전 B/op / allocs | 적용 후 B/op / allocs |
|---|---:|---:|---:|---:|
| 기존 Now benchmark | 44.03 | 44.55 | 0 / 0 | 0 / 0 |
| 기존 Public benchmark | 41.58 | 41.97 | 0 / 0 | 0 / 0 |
| Unix projection | 97.16 | 92.16 | 0 / 0 | 0 / 0 |
| UTC value result | 150.10 | 158.90 | 48 / 1 | 0 / 0 |
| 즉시 성공 CommitWait, Frozen | 163.40 | 29.26 | 248 / 3 | 0 / 0 |

UTC의 약 5.9% 시간 증가는 값 결과와 identity를 복사하는 API 비용을 포함한다. heap allocation을 제거한 대신 모든 호출이 빨라졌다고 주장하지 않는다. 무잠금 Frozen Now 격리 측정은 31.49→36.46ns로 약 5ns 증가해 조사 기준을 넘었다. 초기 통합 CPU profile은 propagation 33.23%, estimate evaluation 14.73%, guard read 7.52% cumulative였다. 최종 측정에도 새 blocking lock/heap allocation은 없으며 안전성 검사를 제거하지 않고 이 비용을 수용한다.

실제 SystemRawClock의 Now/Public/PastWatermark는 workers=1/4/8에서 모두 유효 결과 100%, 0B/op, 0 allocs/op를 기록했다. 수정 전 오류 경로와의 병렬 ns/op 비율은 개선/퇴보 판정에 사용하지 않는다.

| 실제 SystemRawClock | 1 worker ns/op | 4 workers ns/op | 8 workers ns/op |
|---|---:|---:|---:|
| Now | 48.43 | 45.35 | 81.54 |
| Public | 47.85 | 47.15 | 96.72 |
| PastWatermark | 41.80 | 55.06 | 95.87 |

동일한 2백만 호출/worker, 1만 warmup, 1/64 sampling, 3회 반복의 System p99 중앙값:

| API | 1 worker | 4 workers | 8 workers |
|---|---:|---:|---:|
| Now | 0.084µs | 0.417µs | 1.250µs |
| Public | 0.084µs | 0.500µs | 1.334µs |

모든 샘플링 workload에서 invalid 결과가 0이었다. timer/OS scheduling을 포함한 closed-loop 관측이며 다른 플랫폼의 SLO를 보장하지 않는다. Linux/실제 suspend/VM restore는 이번 로컬 실행에서 관찰하지 않았다. 신규 NTS response fuzz는 CI workflow에도 추가했지만 원격 CI 실행 결과를 주장하지 않는다.

재실행용 적용 후 benchmark 코드·CLI smoke·원시 결과·요약은 `local://gstime-applied-evidence.zip`에 보관한다. 저장소의 `remediation_test.go`는 여덟 API의 무할당 계약을 회귀로 검증한다. formatting 문자열과 실제 blocking wait는 무할당 계약 밖이다.
