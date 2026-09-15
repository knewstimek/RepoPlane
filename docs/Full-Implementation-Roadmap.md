# RepoPlane Full Implementation Roadmap

상태: Complete 1.0
기준선: [MVP 1.0 명세](MVP-Spec.md)

이 문서는 1.0 완료 기준선으로 동결됐다. 이후 runtime 권한과 live service 설정은
[Runtime Access 명세](Runtime-Access-Spec.md)와
[Runtime Configuration 명세](Runtime-Configuration-Spec.md)가 현재 상태를 관리한다.

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
| 6 | Conservative Cache | 검증된 순수 변환만 재사용 | eligibility와 false-hit gate | 완료 |
| 7 | Search Adapters | Git history, symbol, frontmatter, JSON/log 확장 | 결과 evidence class | 완료 |
| 8 | HTTP/Auth | HTTP MCP, workspace 권한, 감사와 limits | host 인증 연동 | 완료 |

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

6단계에서 작성하고 승인한 문서:

- [Cache-Spec.md](Cache-Spec.md)
- [Cache-Implementation-Plan.md](Cache-Implementation-Plan.md)
- [ADR-0004](adr/0004-conservative-cache-qualification.md)

7–8단계에서 작성하고 승인한 문서:

- [Search-Adapters-Spec.md](Search-Adapters-Spec.md)
- [Search-Adapters-Implementation-Plan.md](Search-Adapters-Implementation-Plan.md)
- [HTTP-Security-Spec.md](HTTP-Security-Spec.md)
- [HTTP-Implementation-Plan.md](HTTP-Implementation-Plan.md)
- [ADR-0005](adr/0005-search-adapter-evidence-boundaries.md)
- [ADR-0006](adr/0006-http-resource-server-boundary.md)

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

## 8. Conservative Cache 완료

- 별도 `--enable-cache` opt-in과 manifest의 `disabled|observe|verified` policy를 함께 요구한다.
- HMAC key는 manifest source/revision, ordered argv, configuration, executable/runtime identity와
  선언 input hash만 포함하며 로컬 값이나 무관한 Git 상태를 노출하지 않는다.
- observe는 자동 승격하지 않고 동일 key의 output mismatch를 격리한다. verified reuse는
  current/passed qualification record와 artifact hash를 prepare/execute에서 다시 확인한다.
- cache miss, bypass, qualification 부재와 corruption은 등록 실행 자체를 막지 않는다.
- `missing_or_matching`은 다른 content를 덮어쓰지 않으며 `replace_isolated_root`는 untracked,
  input-disjoint 전용 directory만 staged swap한다.
- 새 `run-receipt.v2`가 실행과 `reused`를 구분하고 cache entry pin/TTL/복구를 회귀 test로
  고정했다. 기존 v1 receipt는 계속 읽는다.
- MCP tool은 11개를 유지하고 cache field 추가 뒤에도 compact schema를 30 KiB 이내로
  제한한다. 독립 schema scope를 깨는 외부 `$ref`나 output schema 삭제는 사용하지 않는다.

## 9. Search Adapters 완료

- 기존 `catalog_query`, `workspace_search`, `data_query`만 확장하고 MCP tool은 11개를 유지한다.
- Git commit/path/diff channel, immutable revision과 shallow/partial 상태를 분리한다.
- symbol은 configured `symbol-index.v1`/Universal Ctags JSONL만 읽고 absent/stale/unknown을
  빈 source 검색으로 오인하지 않는다. 범용 parser와 semantic ranking은 만들지 않는다.
- Markdown frontmatter는 strict YAML subset이며 plain Markdown은 catalog source로 오인하지 않는다.
- JSON Pointer, JSONL, log exact/regex, CSV/TSV가 precision, encoding, malformed와 pagination
  계약을 공유한다.
- 추가 contract를 포함한 compact schema는 32 KiB 이내다.

## 10. HTTP/Auth 완료

- stdio가 기본이며 ignored profile의 `--transport http`만 stateless Streamable HTTP를 연다.
- local loopback bearer와 외부 HTTPS OAuth introspection을 지원하고 resource/audience, expiry,
  Host/Origin과 tool별 scope를 확인한다. RepoPlane은 token을 발급하거나 전달하지 않는다.
- 한 process는 한 workspace/resource만 제공한다. public listener는 direct TLS를 요구하고
  loopback reverse proxy의 forwarded header는 권한 판단에 사용하지 않는다.
- request body/header/rate/concurrency/shutdown을 제한하고 disconnect cancellation을 전달한다.
- 별도 bounded `audit.db`는 HMAC identity와 admission/completion metadata만 저장하며 payload,
  token, address와 local path를 저장하지 않는다.

채택된 번호 단계는 모두 완료했다. 이후 작업은 새로운 필수 기능 단계가 아니라 protocol/SDK
호환성 유지, 실제 작업 비교 측정, 선택 adapter와 release maintenance로 관리한다.

## 11. Post-roadmap context efficiency 완료

이 작업은 새 numbered feature stage가 아니라 완료된 11-tool 계약의 호환형 최적화다.

- 전체 schema 직렬화 byte, input 중심 비교 byte, 실제 client/model token과 작업당 비용을
  서로 다른 측정값으로 정의했다.
- 11개 typed tool과 안정적인 `tools/list`, host opt-in, HTTP scope/audit를 유지했다.
- record discovery에는 exact `payload_fields`, mutation에는 opt-in `receipt`를 추가했고 기존
  full 응답은 기본값으로 보존했다.
- 범용 toolbox, 대화 의존적 tool 목록, search grouping과 error envelope 변경은 증거·호환
  설계 없이 도입하지 않았다.
- 유료 model 비교는 수행하지 않았으며 결정론적 계약 검증만 이번 완료 근거로 사용한다.

현재 계약과 보류 조건은 [Context-Efficiency-Spec.md](Context-Efficiency-Spec.md), 실행 이력은
[Context-Efficiency-Implementation-Plan.md](Context-Efficiency-Implementation-Plan.md)에 둔다.

## 12. Post-1.0 durable discovery 완료

이 유지보수 변경은 새 tool이나 numbered stage를 추가하지 않고 `project_records`의 선언된
`search` mode를 실제 bounded lexical 검색으로 완성했다. 검색은 모든 durable record의
문자열 값을 대상으로 하되 compact kind별 payload를 기본 반환하고, memo는 320자 preview만
노출한다. `list`/`get`의 full 기본값, exact `payload_fields`, snapshot cursor와
exact/lower-bound 의미는 유지한다. 현재 계약은 [Records-Spec.md](Records-Spec.md)와
[Context-Efficiency-Spec.md](Context-Efficiency-Spec.md)를 따른다.
