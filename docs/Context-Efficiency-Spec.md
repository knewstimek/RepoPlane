# RepoPlane MCP Context Efficiency Specification

상태: Accepted 1.3
대상: MCP tool discovery, record 응답, 작업당 context 비용

## 1. 목표

RepoPlane은 정확성·권한·호환성 계약을 줄이지 않고 모델이 반복해서 받는 정보만 줄인다.
최적화의 주 지표는 시작 시 schema 크기가 아니라 **성공한 작업당 전체 비용**이다. 전체
비용에는 tool 정의, 호출 인자, 응답, 오류 재시도, 페이지 조회와 재개 비용이 포함된다.

## 2. 서로 다른 측정값

다음 값은 서로 대체할 수 없다.

| 측정값 | 의미 |
|---|---|
| complete contract bytes | 14개 tool의 이름·설명·input/output schema를 compact JSON으로 직렬화한 크기 |
| exposure candidate bytes | 이름·설명·input schema처럼 특정 client 경로가 노출할 수 있는 부분의 직렬화 크기 |
| observed model input tokens | 실제 client가 특정 model 요청에 넣은 정의와 instruction의 token 수 |
| successful-task cost | 작업 완료까지의 전체 token, model step, tool call, 지연과 실패 수 |

[`../schemas/tool-footprint.v1.json`](../schemas/tool-footprint.v1.json)은 앞의 두 **byte**
측정만 재현한다. tokenizer, model, client 변환, native tool search와 cache를 관찰하지 않으므로
byte 수를 token 수나 고정 시작 비용으로 표현하지 않는다.

현재 검증 상태는 schema 중복 설명의 결정론적 축소까지만 완료다. 특정 client가 MCP
`outputSchema` 전체를 model input에 포함하는지, deferred loading 전후 실제 token이 얼마나
달라지는지는 서버가 관찰할 수 없다. 해당 주장은 client별 request trace 또는 같은 작업의
token A/B 측정이 있을 때만 추가하며, API 기능 존재만으로 완료 처리하지 않는다.

## 3. 도구 노출 계약

- 14개 표준 typed tool과 안정적인 `tools/list`를 유지한다. `runtime_access`와
  `runtime_config`는 각각 승인과 live configuration을 담당하는 typed tool이며 별도
  toolbox gateway가 아니라 승인 상태만 관리하는 typed tool이다.
- local stdio runtime grant, 기존 host pre-authorization과 HTTP scope는 서로 독립적이다.
- 대화에서 어떤 tool을 호출했는지에 따라 서버 tool 목록을 바꾸지 않는다.
- 범용 `toolbox(operation, arguments)` gateway는 기본 구조로 사용하지 않는다. 도구별 client
  승인, server scope, audit, SDK 입력 검증과 오류 복구가 동등하다는 별도 증거가 필요하다.
- native deferred exposure/tool search는 client 기능이다. 지원 client는 이를 사용할 수 있지만,
  RepoPlane schema에 비표준 힌트를 넣지 않으며 미지원 client는 직접 tool 계약으로 동작한다.
- 14-tool compact complete-contract 회귀 예산은 34 KiB이고 Runner 세 tool 예산은 5,500
  bytes다.
- Optional host facts는 새 tool/output shape 대신 기존 `memo_write`, `project_records`와
  Runner `checks`를 재사용한다. Typed host input을 추가한 뒤 반복 설명을 줄인 complete
  contract는 34,676 bytes이며, 직전 34,726-byte 기준선보다 50 bytes 작다.
- Optional memo topic identity도 새 tool/output field 없이 `memo_write`의 `topic_key`와 기존
  warning/ref를 재사용한다. 전체 contract는 34,739 bytes, Runner 세 tool은 5,345 bytes이며
  관련 topic은 write 응답에서 최대 3개 key/ref만 노출한다.
- schema identity나 handle은 계약 버전만 식별한다. 압축 뒤 model 문맥에서 사라진 계약 내용을
  복구했다거나 현재 권한을 증명하지 않는다.

## 4. 호환형 compact record 응답

`list`와 `get`의 기존 기본 응답은 바꾸지 않는다. `search`는 발견 전용 compact 기본값을
사용한다.

`project_records`의 `payload_fields`는 반환할 payload의 정확한 최상위 field 이름을 최대
32개까지 선택한다. `list`와 `get`에서 생략하거나 빈 배열이면 전체 payload를 반환한다.
투영 결과는 요약이 아니며 저장된 값을 변경하지 않는다. `payload_complete`는 전체
payload일 때만 `true`다. validity, revision, source, writer class, evidence ref와 pagination
의미는 항상 유지한다.

`project_records(mode=search)`는 하나의 bounded `query` 필드만 추가한다. 검색의 기본
payload는 kind별 핵심 field로 제한하며 memo content는 저장 값을 바꾸지 않는 최대 320자
`preview`로 만든다. 다른 기본 발견 문자열도 320자, 배열은 8개 항목으로 제한한다. 정확한
field나 본문이 필요하면 명시적 `payload_fields` 또는 검색 결과 ID의 `get`을 사용한다. 이
discover-then-get 흐름은 tool schema의 고정 증가보다 반복되는 record 본문 응답 감소를
우선한다.

`checkpoint_write`, `memo_write`, `check_report_import`의 `response_view`는 `full` 또는
`receipt`다. 기본 `full`은 기존처럼 전체 record를 반환한다. `receipt`는 caller가 방금 보낸
payload를 되풀이하지 않지만 ID, kind, schema version, revision, validity, timestamps,
evidence ref, duplicate와 warning을 보존하고 `payload_complete=false`로 표시한다.

cursor-only 다음 페이지는 첫 요청에서 고정된 payload projection을 그대로 사용한다.

## 5. 줄이지 않는 의미와 경계

- `exact`, `lower_bound`, `unknown`; `partial`, `unsupported`; `current`, `stale`, `unknown`,
  `superseded`를 합치지 않는다.
- working tree, Git object, symbol index와 observed/imported/user/LLM evidence를 섞지 않는다.
- snapshot pagination, cursor-only 요청, source 변경·만료 오류와 결과 순서를 보존한다.
- 큰 정수, encoding, 행의 양 끝 포함 범위와 byte의 끝 제외 범위를 보존한다.
- revision 충돌, import idempotency, Runner prepare/execute/revalidation/cache 적격성을 보존한다.
- HTTP auth, scope, host opt-in, stdio runtime grant, workspace 경계와 audit fail-closed 검사를 실제 operation마다 한다.

## 6. 이번 버전에서 채택하지 않은 변경

검색 결과 file grouping은 원문을 보존할 수 있지만 외부 응답 shape와 count 의미를 추가한다.
실제 반복 비용 증거와 기존 항목으로 완전히 펼쳐지는 동등성 설계 전에는 도입하지 않는다.
공개 오류의 상세화도 안정적인 machine code 소비자를 깨지 않는 versioned 구조가 정해질
때까지 보류한다. output schema 삭제, 의미 있는 field 약어화와 지나치게 작은 기본 page도
채택하지 않는다.

별도 Sol/Astra model 비교는 이 버전의 완료 gate가 아니다. 사용자 비용을 쓰지 않는
결정론적 schema·계약·응답 회귀 검증만 수행하며, model별 비열등성을 입증했다고 주장하지
않는다. 향후 model 비교를 할 때는 명시적 예산과 승인 아래 동일 작업의 성공률, 의미 오판,
전체 token, step, call과 latency를 기준선과 짝비교한다.

## 7. 완료 조건

- tool 이름과 HTTP scope가 하나의 source에서 파생되고 모든 공개 tool의 fail-closed mapping이
  test로 고정된다.
- schema와 footprint report가 함께 재생성되고 drift test를 통과한다.
- compact record 옵션의 기본 호환성, lexical search, projection pagination, 유효하지 않은
  값과 실질적인 반복 payload 감소가 test로 고정된다.
- Stage 7/8 의미·권한 회귀와 전체 repository verification/public-release gate가 통과한다.
