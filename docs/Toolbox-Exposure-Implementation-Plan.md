# RepoPlane Lazy Toolbox Exposure Implementation Plan

상태: Proposed
기준선: [Lazy Toolbox Exposure 명세](Toolbox-Exposure-Spec.md)

이 계획은 구현 준비 설계다. 현재 runtime, 기본 14-tool 표면과 설치 실행 파일은 변경하지
않는다. 구현 착수와 model/API 비용이 드는 A/B는 별도 승인 후 진행한다.

## 목표

기존 14개 operation의 concrete Go 계약과 보안 경계를 재사용하면서, opt-in `toolbox.v1`에서
다섯 개의 고정 MCP tool과 lazy operation contract를 제공한다. Tool 목록은 process lifetime
동안 불변이며 schema handle은 contract identity로만 사용한다.

## 실행 보드

| 단계 | 결과 | 완료 gate | 상태 |
|---:|---|---|---|
| 0 | 기준선과 평가 corpus | 현 14-tool footprint, 대표 workflow, trace field와 A/B threshold 승인 | 대기 |
| 1 | 단일 operation registry | 14 operation의 type/scope/annotation/handler가 한 registry에서 typed surface와 일치 | 대기 |
| 2 | contract canonicalization | compact/complete descriptor와 deterministic SHA-256 handle, golden/drift test | 대기 |
| 3 | 다섯 toolbox transport | fixed describe/call envelope, strict decode, stale/cross-operation fail-closed | 대기 |
| 4 | 권한·승인·audit 동등성 | 기존 다섯 scope, runtime elicitation, redacted resolved-operation audit 회귀 | 대기 |
| 5 | harness reference path | automatic handle insertion, active-context residency와 rehydrate를 분리한 reference adapter/test | 대기 |
| 6 | schema와 문서 | typed/toolbox schema·footprint, startup-only host 설정 예제, migration/rollback 문서 | 대기 |
| 7 | 결정론적 verification | focused/full/public-release checks와 설치 전 opt-in smoke test | 대기 |
| 8 | 승인된 client/model A/B | 성공률, token/cache, call, retry와 latency gate로 채택 또는 기각 | 대기 |

## 설계 순서

### 0. 측정 기준선

- `tool-footprint.v1`에 표면별 name/description/input과 complete bytes를 분리한다.
- 기존 durable run/audit에서 payload를 노출하지 않고 operation 빈도와 대표 연속 호출만 bounded 집계한다.
- read discovery, memo/checkpoint, report import, prepare/execute/inspect와 state export fixture를 고정한다.
- model A/B 전에 표본 수, 비용, 성공 판정과 허용 call/latency 증가를 별도 승인한다.

### 1. Operation registry

- `internal/mcpserver`의 반복 `AddTool` metadata를 registry로 옮기되 service는 계속 domain interface에 의존한다.
- 기존 typed surface가 registry에서 생성되는 것을 먼저 검증하고 외부 동작을 바꾸지 않는다.
- operation 누락, 중복 toolbox membership과 `RequiredScope` 불일치는 compile/test 단계에서 실패한다.

### 2. Contract와 handle

- 공개 request/response schema 생성 경로를 재사용해 `operation-contract.v1`을 canonicalize한다.
- Handle payload에 scope와 caller-visible semantic revision을 포함하고 SHA-256 test vector를 고정한다.
- `known_schema_handle`, `unchanged`, `schema_changed`와 compact/full detail의 byte limit을 test한다.
- Handle을 authorization, approval나 model-context residency로 소비하는 코드를 금지한다.

### 3. Toolbox dispatch

- startup-only surface enum으로 typed 또는 toolbox 중 하나만 등록한다.
- 다섯 toolbox의 operation allowlist를 registry에서 생성하고 목록/순서를 고정한다.
- Call은 handle 확인 뒤 기존 concrete request로 strict decode하고 기존 handler/service path를 호출한다.
- 기존 output을 공통 envelope에 넣되 concrete response validation을 유지한다.

### 4. Security parity

- HTTP middleware는 toolbox 이름으로 기존 단일 scope를 fail closed 적용한다.
- stdio runtime access, approval resume와 Runner revalidation을 operation dispatch 뒤에도 그대로 수행한다.
- Audit admission/completion에 resolved operation의 bounded identity만 추가한다.
- Unknown/stale/cross-toolbox handle, malformed arguments와 unauthorized describe/call을 집중 검증한다.

### 5. Harness와 compaction

- Reference harness cache key는 server identity, surface version, toolbox, operation과 handle이다.
- Model이 hash를 복사하지 않아도 call에 current handle을 삽입하는 adapter test를 둔다.
- Contract cache와 active model context residency를 별도 상태로 추적한다.
- Compaction/new-thread simulation에서 cached descriptor 재주입 또는 full re-describe를 요구하고
  `unchanged`만 반환해 schema가 사라지는 경로를 금지한다.
- 지원 client에 integration point가 없으면 해당 limitation을 기록하고 toolbox를 experimental로 유지한다.

### 6. 공개 계약과 사용법

- versioned toolbox schema와 footprint를 기존 schema generator에서 생성한다.
- `--tool-surface typed.v1|toolbox.v1` 같은 최종 startup 설정은 구현 시 CLI naming 검토를 거친다.
- 설정은 ignored host profile에 두고 tracked 문서에는 copyable neutral 예제만 둔다.
- 세션 중 변경 불가, 새 연결 rollback과 typed 기본값을 README에 명시한다.

### 7. Verification과 배포

- 변경 package를 focused compile/test한 뒤 repository verification을 한 번 수행한다.
- 최종 schema 변경 뒤 full verification을 다시 한 번 수행한다.
- public tree/history privacy scan을 통과한 뒤에만 commit/push한다.
- MCP 실행 파일이 바뀐 구현 slice에서만 설치본을 안전하게 교체한다. 이 설계-only slice에서는 교체하지 않는다.

### 8. A/B와 결정

- 같은 model/toolchain/workspace fingerprint와 workflow corpus로 typed/toolbox를 짝비교한다.
- observed input/cached/output token, calls, validation retry, approval 오류와 latency를 수집한다.
- 결정론적 bytes와 observed model tokens를 섞지 않는다.
- gate 통과 시에도 default 변경은 별도 호환성 결정으로 남긴다. 실패하면 typed 기본을 유지하고
  toolbox experiment를 제거하거나 명시적 experimental surface로 제한한다.

## 중단 조건

- 지원 client에서 fixed toolbox보다 operation contract를 cache-safe하게 append할 수 없음
- Approval UI나 HTTP scope가 resolved operation을 안전하게 표현하지 못함
- 첫 call 정확도 또는 successful-task cost가 typed 기준보다 열화
- Operation registry가 typed와 toolbox 사이에 독립 계약 source를 만들게 됨
- Compaction 뒤 schema residency를 증명하거나 rehydrate할 수 없음

