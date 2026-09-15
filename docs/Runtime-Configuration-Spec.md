# RepoPlane Runtime Configuration Specification

상태: Accepted 1.1  
대상: local stdio MCP의 live source와 service topology

## 1. 목표

RepoPlane을 MCP client에 한 번 등록한 뒤 catalog/candidate/rule/symbol source, primary
workspace, state directory, HTTP profile을 바꾸기 위해 host TOML을 편집하거나 stdio
process를 재시작하지 않는다. CLI와 host 설정 인자는 시작 시 초기값을 사전등록하는
호환 경로로만 유지한다.

## 2. 공개 계약

`runtime_config`는 항상 발견되는 typed tool이며 다음 요청을 제공한다.

- `status`: 현재 `configuration` map을 반환하며 승인을 요구하지 않는다.
- source target `catalog_root`, `candidate_root`, `rule_file`, `symbol_index`:
  `add`, `remove`, `replace`, `refresh`.
- `workspace`, `state_dir`: 절대 경로 하나를 받는 `select`.
- `http_transport`: ignored local HTTP profile 절대 경로 하나를 받는 `start`, 또는 `stop`.

source path는 selected workspace 기준 상대 경로다. `add`/`replace` 대상은 승인 시점에
존재해야 한다. `replace`의 `values`는 빈 배열을 포함한 전체 ordered set이다. 응답의
`changed`는 설정 값 변경, `refreshed`는 새 service bundle의 catalog refresh 완료를 뜻한다.
`configuration.http_transport`는 중지 상태에서 빈 배열, 실행 중 profile path 하나다.

## 3. 승인과 원자성

- `status` 외 모든 요청은 local stdio MCP elicitation을 한 번 거친다.
- 확인 전용 요청도 `mode=form`과 빈 object `requestedSchema`를 명시한 유효한
  `elicitation/create` wire shape를 사용한다.
- server-issued one-time state는 action, target, 전체 values를 묶는다. 재개 요청이 다른
  값을 보내도 저장된 proposal만 실행하며 decline, cancel, expiry, reuse는 fail closed다.
- 새 source/workspace/state 설정은 별도 workspace root, DB handles, cursor/cache keys,
  catalog/search/path/record/Runner/backup service bundle로 먼저 연다. catalog refresh와 Runner
  recovery까지 성공한 뒤 router pointer를 교체한다.
- 교체 중 기존 요청은 기존 bundle에서 끝나거나 새 bundle에서 시작한다. active Runner나
  memory snapshot은 `runtime_configuration_busy`로 교체를 막는다.
- state directory는 선택된 workspace 밖이어야 한다. workspace 변경 시 외부 read grant와
  pending approval을 폐기하고 기능 grant만 유지한다.

## 4. HTTP 경계

stdio에서 시작한 HTTP는 child transport이며 start/stop이 stdio를 끊지 않는다. profile,
token, TLS와 OAuth 검증은 기존 HTTP 보안 명세를 그대로 적용하고 listener bind까지 성공해야
start가 완료된다. active HTTP 상태에서 workspace/state를 교체하면 endpoint를 graceful
shutdown하고 새 bundle의 audit store로 다시 연다. 실패하면 이전 bundle과 endpoint 복구를
시도한다.

HTTP `tools/list`도 같은 tool set을 유지하지만 `runtime_config` 호출은
`runtime_configuration_unavailable`로 거부한다. HTTP principal은 local stdio topology나
stdio runtime lease를 제어하거나 공유하지 않는다.

## 5. 검증 조건

- 한 MCP session에서 승인 후 source add가 즉시 `catalog_query` 결과에 반영된다.
- 네 source target의 status, add/remove/replace/refresh가 재시작 없이 동작한다.
- workspace/state switch 후 search와 durable DB가 새 bundle을 사용한다.
- HTTP listener start/stop이 stdio를 유지하고 HTTP에서는 runtime configuration을 거부한다.
- 실패한 준비는 이전 bundle을 보존하고 schema, race, HTTP auth, privacy 검증이 통과한다.
