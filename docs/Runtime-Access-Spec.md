# RepoPlane Runtime Access Specification

상태: Accepted 1.1  
대상: local stdio MCP의 작업 중 권한 승인과 workspace 외부 읽기

## 1. 문제와 목표

RepoPlane 1.0은 workspace와 mutation/Runner/cache 권한을 프로세스 시작 인자로만
고정했다. 에이전트가 작업 중 필요한 상위 파일이나 기능을 발견하면 사용자가 MCP host
설정을 수정하고 서버를 재시작해야 했다. 1.1은 local stdio 세션에서 해당 호출을
중단하지 않고 사용자 승인을 받은 뒤 같은 호출을 재개한다.

서버를 MCP client에 최초 등록하는 host 설정은 이 계약의 대상이 아니다. 한 번 연결된
RepoPlane의 읽기·쓰기·실행 권한을 바꾸기 위해 TOML을 다시 수정하거나 서버를 재시작할
필요는 없다.

## 2. 공개 계약

표준 typed tool은 13개이며 `tools/list`는 대화 상태에 따라 변하지 않는다.

- `runtime_access`는 `status`, `grant`, `revoke`를 제공한다.
- `grant` kind는 `read_path`, `intent_write`, `report_import`, `runner_execute`,
  `cache_reuse`, `memory_export`다.
- `read_path`는 primary workspace 기준 상대 경로만 받는다. `../`는 허용하지만 절대
  경로는 받지 않는다.
- `memory_export`는 portable memory archive를 쓸 정확한 절대 destination directory를
  승인하며, archive 포맷과 복원 경계는 [Memory Backup 명세](Memory-Backup-Spec.md)를 따른다.
- `checkpoint_write`, `memo_write`, `check_report_import`, `run_prepare`, `run_execute`,
  `run_inspect`는 필요한 grant가 없으면 호출 안에서 승인을 요청한다.
- `workspace_search`, `path_explain`, `data_query`가 primary workspace를 벗어나면 요청한
  기존 파일 또는 디렉터리에 대한 read-only 승인을 요청한다.

승인은 MCP multi round-trip `input_required`/elicitation으로 전달한다. 승인 요청은 권한을
바꾸지 않으며, client가 `accept`를 반환하고 서버가 발급한 단기 one-time request state를
검증한 뒤에만 grant를 적용한다. `decline`, `cancel`, 위조·재사용·만료 state는 fail closed다.

## 3. 권한 경계

- runtime grant는 local stdio 프로세스에만 적용되고 영속 저장하지 않는다. stdio 연결이
  끝나 프로세스가 종료되면 모두 사라진다.
- stateless HTTP는 runtime grant를 지원하지 않는다. 기존 host opt-in, bearer/OAuth scope,
  audit 경계를 유지한다.
- 외부 read grant는 `workspace_search`, `path_explain`, `data_query`의 조회 해석에만
  추가된다.
- catalog discovery, report/checklist import, Runner executable/input/output/cwd, cache
  materialization은 항상 immutable primary workspace 경계를 사용한다.
- 외부 디렉터리 grant도 canonical real path와 symlink/junction 경계를 검사한다. grant
  밖의 sibling이나 parent는 계속 `workspace_escape`다.
- external-read 응답과 schema는 canonical absolute host path를 노출하지 않는다. read grant에는
  불투명 ID와 caller가 요청한 정규화 상대 경로만 기록한다. `memory_export`는 local caller가
  직접 제공하고 승인한 exact canonical destination만 session grant에 보존한다.

## 4. 시작 플래그 호환성

기존 `--enable-intention-writes`, `--enable-report-import`, `--enable-runner`,
`--enable-cache`는 제거하지 않는다. 1.1부터 이 플래그는 도구 노출 여부가 아니라 해당
기능을 runtime prompt 없이 **사전 승인**한다. 설정하지 않은 local stdio 기능도 도구는
안정적으로 노출되며 첫 실제 호출에서 승인을 요청한다.

`--enable-cache`는 계속 `--enable-runner`를 요구한다. runtime에서는 두 grant가 독립적이며
Runner만 승인된 경우 cache decision은 `runtime_cache_not_approved`로 fail closed다.

## 5. 제한과 오류

- 동시 pending approval은 64건, 수명은 5분이다.
- `runtime_access_unavailable`: transport가 runtime grant를 허용하지 않는다.
- `runtime_approval_invalid`: approval state가 없거나 만료·재사용되었다.
- 사용자 거부는 `permission_denied`다.
- 존재하지 않는 read target은 grant하지 않는다.

## 6. 검증 조건

- 설정 플래그 없는 stdio application에서 외부 파일 read와 intention write가 각각 한 번의
  elicitation 후 같은 tool call 안에서 완료된다.
- proposal, decline, expiry, revoke는 권한을 남기지 않는다.
- 외부 read grant가 catalog, importer, Runner와 cache write 경계를 넓히지 않는다.
- HTTP authorization, public error sanitization, schema drift와 contract footprint 회귀가
  통과한다.
