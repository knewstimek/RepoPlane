# RepoPlane Lazy Toolbox Exposure Specification

상태: Draft 0.1 (미구현, opt-in 후보)
대상: MCP tool discovery, lazy operation contract, schema handle, prompt-cache 안정성

## 1. 목적

RepoPlane은 현재 14개 self-contained typed MCP tool을 안정적으로 노출한다. 이 명세는 기존
표면을 제거하지 않고, 동일한 내부 request/response·권한·승인 계약을 다섯 개의 고정 toolbox와
필요 시 조회하는 operation contract로 제공할 수 있는지 정의한다.

목표는 다음과 같다.

- 한 연결의 `tools/list`를 수명 내내 고정해 prompt prefix를 다시 쓰지 않는다.
- 사용하지 않는 operation의 JSON Schema를 model context에 선행 적재하지 않는다.
- 기존 concrete Go request/response 타입, limit, cursor, partial state와 오류 의미를 재사용한다.
- operation별 HTTP scope, stdio runtime approval, audit와 workspace 경계를 약화하지 않는다.
- schema를 이미 본 caller는 content-bound handle로 동일성을 짧게 확인할 수 있다.
- 실제 client trace에서 successful-task cost가 줄었을 때만 기본 표면 변경을 검토한다.

이 명세는 client-native tool search를 구현했다고 주장하지 않는다. MCP client가 RepoPlane의
lazy contract를 특별히 이해하지 않아도 명시적인 `describe`와 `call`로 동작하는 application
protocol 후보를 정의한다.

## 2. 비목표

- 대화 내용, 승인 상태나 이전 호출에 따른 자동 profile 승격
- `notifications/tools/list_changed`를 사용한 런타임 도구 추가·제거
- 자유 형식 shell, executable, argv 또는 SQL gateway
- schema handle을 권한, 승인, freshness나 model 기억의 증명으로 사용
- 기존 typed MCP 표면의 즉시 제거 또는 자동 migration
- 외부 `$ref`를 client가 해석한다고 가정하는 cross-tool schema deduplication

## 3. 표면과 권한 경계

`toolbox.v1` 표면은 프로세스 시작 시 선택하며 연결 수명 동안 정확히 다음 다섯 tool을 같은
순서와 schema로 유지한다.

| Toolbox | 기존 operation | 요구 scope |
|---|---|---|
| `repoplane_read` | `catalog_query`, `workspace_search`, `path_explain`, `data_query`, `project_records`, `runtime_access` | `repoplane.read` |
| `repoplane_write` | `checkpoint_write`, `memo_write` | `repoplane.intent.write` |
| `repoplane_import` | `check_report_import` | `repoplane.report.import` |
| `repoplane_runner` | `run_prepare`, `run_execute`, `run_inspect` | `repoplane.runner.execute` |
| `repoplane_state` | `runtime_config`, `memory_backup` | `repoplane.state.export` |

이 분리는 현재 `RequiredScope`의 fail-closed authorization class와 같다. Toolbox 이름만으로
HTTP admission scope를 결정할 수 있어 request body를 인증 middleware에서 미리 해석하거나
여러 scope를 하나로 넓힐 필요가 없다. `runtime_access`의 grant/revoke는 기존처럼 explicit
elicitation과 capability 검사를 추가로 거치며 read scope 자체를 grant 증명으로 보지 않는다.

기본 `typed.v1` 표면은 기존 14개 tool을 그대로 유지한다. 선택 값은 startup-only
`typed.v1|toolbox.v1`이며 `runtime_config`, 승인 grant나 operation 호출로 바꿀 수 없다. 한
서버가 두 표면을 동시에 노출하지 않는다.

## 4. 고정 toolbox 요청 계약

각 toolbox는 동일한 작은 envelope를 사용하되 `operation` enum은 해당 toolbox의 allowlist로
제한한다.

```json
{
  "action": "describe | call",
  "operation": "workspace_search",
  "known_schema_handle": "optional, describe only",
  "schema_handle": "required for call",
  "arguments": {}
}
```

- `describe`는 `operation`을 요구하고 `known_schema_handle`을 선택적으로 받는다.
- `call`은 `operation`, 현재 `schema_handle`과 object `arguments`를 요구한다.
- action에 맞지 않는 field, unknown field와 해당 toolbox에 속하지 않는 operation은 거부한다.
- `arguments`는 자유 JSON으로 실행되지 않는다. 선택한 operation의 기존 concrete request
  타입으로 unknown field를 금지해 decode한 뒤 기존 service를 호출한다.
- cursor-only, required/default/limit와 cross-field validation은 기존 operation과 동일하다.
- toolbox envelope와 operation argument의 byte/depth/item/deadline limit를 각각 둔다.

MCP output schema는 모든 기존 output의 거대한 `oneOf`를 포함하지 않는다. 공통 envelope의
`status`, `operation`, handle, warning과 generic `result`만 선언한다. 서버 test는 `result`를
기존 concrete response 타입과 공개 schema로 계속 검증한다.

## 5. Describe 계약

처음 또는 rehydrate가 필요한 caller는 handle 없이 describe한다.

```json
{
  "action": "describe",
  "operation": "workspace_search"
}
```

기본 응답은 model이 올바른 첫 call을 만들 수 있는 `operation-contract.v1`을 반환한다.

```json
{
  "status": "ok",
  "operation": "workspace_search",
  "schema_handle": "schema:workspace_search@sha256:...",
  "contract": {
    "input_schema": {},
    "result_summary": {},
    "scope": "repoplane.read",
    "side_effect": "read_only",
    "approval": "conditional",
    "retry": {}
  }
}
```

compact contract에도 다음 의미를 생략하지 않는다.

- field type, required, default, enum, numeric/string/array limit
- cursor-only와 mutually exclusive/cross-field 규칙
- exact/lower-bound/unknown 및 partial/unsupported 의미
- side effect, 요구 scope와 추가 runtime approval 가능성
- 대표 public error code와 안전한 retry 또는 re-describe 절차
- 결과의 discriminator, pagination, validity와 payload completeness 요약

SDK나 contract audit를 위한 `detail=complete`는 기존 complete input/output schema를 bounded
응답으로 제공할 수 있다. model 기본 경로는 compact contract이며 complete contract를 보았다고
간주하지 않는다.

## 6. Schema handle과 unchanged

`schema_handle`은 canonical `operation-contract.v1` bytes의 SHA-256 identity다. canonical
payload에는 toolbox/operation 이름, argument schema, result contract revision, scope,
side-effect/approval metadata와 retry semantics revision을 포함한다. 유효한 입력이나 caller가
의존하는 의미가 바뀌면 handle도 바뀐다.

Handle은 다음을 증명하지 않는다.

- caller가 현재 contract 내용을 model context에 보유함
- caller가 operation을 호출할 권한이나 runtime grant를 보유함
- workspace, source, record나 run 상태가 이전 describe 시점과 같음
- 이전 호출 결과가 current함

Caller가 같은 contract를 active context에 보유하거나 cached contract를 다시 model에 주입할
수 있을 때만 `known_schema_handle`을 보낸다. 일치하면 서버는 schema를 반복하지 않는다.

```json
{
  "status": "unchanged",
  "operation": "workspace_search",
  "schema_handle": "schema:workspace_search@sha256:..."
}
```

`call`의 handle이 stale하거나 다른 operation/toolbox에 속하면 실행 전에 fail closed한다.
복구 가능한 `schema_changed` 결과는 current handle과 새 compact contract를 함께 반환해 별도
describe round trip 없이 caller가 검토 후 재시도할 수 있게 한다. Stale request를 새 schema로
암묵적으로 재해석하지 않는다.

## 7. Harness, history와 compaction

서버는 client의 prompt layout, cache breakpoint, compaction 또는 model 기억을 관찰할 수 없다.
따라서 handle reuse에는 세 수준을 구분한다.

1. **Harness-managed:** client가 `(server identity, surface version, toolbox, operation)`별 contract와
   handle을 저장하고 call에 handle을 자동 삽입한다. Hash 문자열 관리를 model에 맡기지 않는다.
2. **Model-visible fallback:** 특별한 harness가 없으면 model이 describe 결과의 handle을 같은
   대화에서 call에 전달한다. 이 경로도 동작해야 하지만 최적 경로로 간주하지 않는다.
3. **Rehydrate:** compaction이나 새 thread 뒤 model-visible contract가 보존됐다는 증거가 없으면
   harness cache에 handle이 있어도 `unchanged`만 model에 보여 주지 않는다. Cached contract를
   context 끝에 재주입하거나 handle 없이 describe해 full compact contract를 다시 받는다.

Harness cache와 model context residency는 별도 상태다. `known_schema_handle`은 두 상태를
혼동하지 않으며, 서버가 compaction을 추측해 자동으로 full/unchanged를 선택하지 않는다.

Describe 결과는 일반 tool result로 history suffix에 추가되므로 고정 tool prefix를 바꾸지
않는다. 그러나 한 작업이 많은 operation을 describe하면 schema bytes가 history에 누적될 수
있다. Lazy 표면은 초기 bytes 감소만으로 성공 처리하지 않고 다음을 실제 trace로 측정한다.

- 작업당 distinct described operation 수와 schema bytes
- compaction 전후 rehydrate 횟수와 bytes
- cached/uncached input tokens
- describe를 포함한 total calls와 latency
- 첫 call validation 성공률과 schema_changed retry

## 8. Authorization, approval와 audit

- Toolbox admission은 표의 단일 기존 scope를 요구한다.
- `call`은 기존 operation의 authorization, runtime capability, path와 workspace 검사를 다시 한다.
- `describe`도 해당 toolbox scope 없이 operation 정보를 노출하지 않는다.
- Audit에는 toolbox와 resolved operation을 모두 기록하되 arguments, schema나 secret은 기록하지 않는다.
- Approval UI가 toolbox 이름만 표시하더라도 elicitation에는 resolved operation, side effect와
  bounded target summary가 나타나야 한다.
- Unknown toolbox/operation과 registry drift는 fail closed한다.
- Runner는 계속 registered capability와 typed arguments만 받고 request-supplied command,
  executable이나 argv를 허용하지 않는다.

## 9. Registry와 drift 방지

Typed와 toolbox 표면이 각자 schema나 handler를 복제하지 않는다. 하나의 operation registry가
다음을 소유한다.

- public operation 이름과 toolbox class
- 기존 request/response concrete type adapter
- scope, annotation과 approval class
- compact/complete contract generator
- handler dispatch와 audit operation identity

기존 14개 typed tools와 다섯 toolbox descriptor는 이 registry에서 생성한다. 모든 기존 public
operation이 정확히 한 toolbox에 속하고 scope가 `RequiredScope`와 일치하며, 두 표면의 호출이
동일 service 결과와 public error를 내는지 test한다.

## 10. 호환성과 채택 gate

첫 구현에서도 `typed.v1`은 기본값이고 `toolbox.v1`은 명시적 opt-in이다. 다음 deterministic
gate를 모두 통과해야 실험 배포할 수 있다.

- 다섯 toolbox의 이름+설명+input schema compact 합계가 5 KiB 이하
- toolbox `tools/list`가 grant, call, runtime configuration과 시간 경과 전후 byte-for-byte 동일
- 14개 operation의 typed/toolbox 성공 응답과 public error 의미가 동등
- scope, approval, audit, unknown field, stale/cross-operation handle이 fail closed
- committed schema/footprint와 source registry drift test 통과
- focused package와 전체 repository verification 통과

기본값 변경은 별도의 client/model A/B gate다. 대표 read, record write, verification Runner,
import와 state 작업을 같은 입력으로 비교해 다음을 만족해야 한다.

- task success와 authorization 정확성 비열등
- 첫 call validation 성공률 비열등
- median successful-task input tokens가 의미 있게 감소
- cache hit 감소가 초기 schema 절약을 상쇄하지 않음
- 추가 describe/retry call과 p95 latency가 사전에 정한 예산 이내

Model A/B의 표본, 비용과 threshold는 실행 전에 명시적으로 승인한다. API 기능 존재나
결정론적 byte 감소만으로 token·정확성 개선을 주장하지 않는다.

