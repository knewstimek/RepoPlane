# RepoPlane Full Implementation Roadmap

상태: Active 0.1
기준선: [MVP 1.0 명세](MVP-Spec.md)

## 1. 완료 범위

RepoPlane의 "풀 구현"은 장기 설계에서 **채택된 P1/P2 기능을 모두 구현하고 검증한
상태**를 뜻한다. 기획 과정에서 검토한 모든 기능을 뜻하지 않는다.

다음 항목은 풀 구현의 필수 범위가 아니다.

- 중앙 오케스트레이터
- 범용 dependency/integration graph
- 모든 실행을 관찰하는 전역 shell wrapper
- 전용 graph database, embedding service, 상시 추론 모델
- 임의 명령 또는 임의 shell 문자열 실행

완료된 조회 전용 MVP의 외부 계약은 [MVP-Spec.md](MVP-Spec.md)에 동결한다. 이후
기능은 독립된 수직 절단과 versioned schema로 추가하며, MVP 문서를 미래 요구사항의
작업 보드로 다시 사용하지 않는다.

## 2. 수직 절단과 순서

| 순서 | 수직 절단 | 주요 결과 | 시작 전 결정 | 상태 |
|---:|---|---|---|---|
| 1 | Records/Verification | durable record 조회, verification validity | write 권한과 revision 계약 | 완료 |
| 2 | Checkpoint/Memo/Importer | 의도 기록, 실패·결정 메모, CI report import | writer 종류와 provenance | 완료 |
| 3 | Environment Preflight | executable/SDK/Git/config 전제조건 관찰 | 허용 probe와 비밀정보 경계 | 완료 |
| 4 | Artifact/Run Receipt | run/input/output/reference, hash, 보존·redaction | blob 보존과 삭제 정책 | 완료 |
| 5 | Prepare/Execute/Inspect | 제한된 capability 실행과 취소 | 승인 주체와 OS 격리 한계 | 완료 |
| 6 | Conservative Cache | 검증된 순수 변환만 재사용 | eligibility와 false-hit gate | 대기 |
| 7 | Search Adapters | Git history, symbol, frontmatter, JSON/log 확장 | 결과 evidence class | 대기 |
| 8 | HTTP/Auth | HTTP MCP, workspace 권한, 감사와 limits | host 인증 연동 | 대기 |

Runner는 Records와 Preflight가 완료되기 전에 시작하지 않는다. Cache는 Runner와
artifact validity가 안정된 뒤에만 시작한다. HTTP 배포는 local stdio 계약과 권한
모델을 충분히 검증한 뒤 진행한다.

## 3. 명세 전략

지금 유지하는 문서:

- 이 문서: 채택 범위, 순서, 단계별 gate
- [Records-Spec.md](Records-Spec.md): 완료된 Records/Verification 및
  Checkpoint/Memo/Importer 수직 절단의 외부·저장 계약
- [ADR-0002](adr/0002-record-write-boundaries.md): read/write 책임과 충돌 정책

3–5단계에서 작성하고 승인한 문서:

- [Preflight-Spec.md](Preflight-Spec.md)
- [Artifact-Provenance-Spec.md](Artifact-Provenance-Spec.md)
- [Runner-Spec.md](Runner-Spec.md)
- [ADR-0003](adr/0003-runner-authority-and-compatibility.md)

다음 문서는 해당 수직 절단을 시작할 때 작성하고 승인한다.

- `Cache-Spec.md`
- `Search-Adapters-Spec.md`
- `HTTP-Security-Spec.md`

빈 명세를 미리 만들지 않는다. 앞 단계에서 검증한 계약과 실패 사례를 다음 명세의
입력으로 사용한다.

## 4. 공통 완료 게이트

각 수직 절단은 다음 조건을 모두 만족해야 완료다.

- public schema, domain interface, adapter conformance test가 함께 변경됨
- bounded read/write와 `exact`/`lower_bound`/`unknown` 의미가 유지됨
- workspace와 record 접근 권한이 명시적으로 거부되는 회귀 test가 있음
- partial, stale, missing, redacted 상태가 성공이나 보존됨으로 오인되지 않음
- Windows와 Linux의 지원 범위 및 미지원 보안 경계가 문서화됨
- 로그와 fixture에 credential, 로컬 identity, 절대 경로가 없음
- 기존 MVP tool의 read-only 동작과 stdout protocol 회귀 test가 통과함

## 5. Records 수직 절단 완료 조건

첫 수직 절단은 [Records-Spec.md](Records-Spec.md)를 구현하고 다음을 증명한다.

- 재생성 cache와 durable record가 물리적 복구 경계로 분리됨
- `project_records`는 조회만 수행함
- server/importer와 사용자 의도 writer의 권한이 record kind별로 분리됨
- 수정 가능한 record는 `expected_revision` compare-and-swap을 사용함
- verification 결과가 대상 revision과 scope 변화에 따라 current/stale/unknown으로 계산됨
- report import가 중복에 안전하고 원본·parser provenance를 보존함
- conflict, stale, skipped, report missing, partially observed 사례가 회귀 test로 고정됨

## 6. 자동화가 만드는 연결점

개발 자동화는 Runner의 우회 구현이 아니다. 사용자가 직접 실행하는 제한된 개발
명령이며 결과는 ignored local directory에 bounded JSON report로 기록한다.

- `go run ./cmd/repoplane-dev verify`: schema/test/vet/diff/build 결과를 `check-report.v1`로 생성
- `go run ./cmd/repoplane-dev preflight`: 값 없이 executable, SDK, Git, 전제조건 확인
- `go run ./cmd/repoplane-dev public-release-check`: tracked tree와 Git history 공개 검사

Records importer는 같은 report schema를 입력으로 사용한다. 자동화의 존재나
exit code만으로 verification을 passed로 만들지 않고, 요구된 check와 report completeness를
함께 검사한다.

## 7. Execution Foundation 완료

3–5단계는 하나의 호환성 우선 수직 절단으로 완료했다.

- 조회 tool 다섯 개는 기본 노출을 유지하고 Runner opt-in 시 실행 tool 세 개만 추가한다.
- environment preflight는 `run_prepare`에 포함하며 별도 실행 tool을 만들지 않는다.
- required check만 실행을 막고 recommended/informational 결과는 명시적 warning으로 남긴다.
- run/environment/artifact record는 기존 `records.db` v1에 additive kind로 저장한다.
- plan은 manifest revision, executable identity와 선언 input만 재검사하므로 무관한 변경은
  실행을 막지 않는다.
- stdout/stderr pagination, timeout/cancel/restart interruption, artifact 한도·누락·변조,
  stream retention과 reference 보호를 회귀 test로 고정했다.
- Windows batch wrapper와 POSIX shebang은 OS adapter 뒤에서 실행하고 cache는 사용하지 않는다.
