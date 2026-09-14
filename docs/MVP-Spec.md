# RepoPlane MVP 명세

상태: MVP 1.0
구현 언어: Go
초기 transport: stdio

## 1. 목적

첫 MVP는 프로젝트의 기존 도구와 원문을 작고 검증 가능한 응답으로 찾는
**조회 전용 기반**을 제공한다. 실행 runner, 캐시, 자동 provenance보다 검색 범위와
불완전 상태를 정직하게 표현하는 공통 계약을 먼저 안정화한다.

## 2. 포함 범위

첫 수직 절단(vertical slice)은 다음 네 tool을 제공한다.

| Tool | MVP mode | 책임 |
|---|---|---|
| `catalog_query` | `search`, `get`, `list`, `audit`, `status` | 선언 항목 검색, 조회, 순회, 누락 후보와 색인 상태 |
| `path_explain` | 단일 조회 | workspace 경계, Git 상태, 실제 경로, 규칙 후보, 인코딩·newline 관찰 |
| `workspace_search` | `filename`, `exact`, `regex` | 고정된 scope의 파일 및 내용 검색 |
| `data_query` | `text_range`, `jsonl` | 원문 범위와 제한된 JSONL filter/projection |

지원 원본:

- workspace 내부 일반 파일
- Git metadata가 있을 때 현재 worktree의 tracked/ignored 상태
- YAML 또는 JSON catalog manifest
- UTF-8, UTF-8 BOM, CP949, EUC-KR의 명시적 decode 요청
- BOM과 유효성 검사에 근거한 제한적 encoding 관찰

MVP에서 지원하지 않는 항목:

- capability 실행과 `run_prepare`, `run_execute`, `run_inspect`
- semantic 또는 symbol 검색
- Git history 검색
- XML, CSV/TSV, 임의 JSONPath/JMESPath
- 자동 encoding 확정 또는 파일 편집
- verification/checkpoint/run record 쓰기와 조회
- artifact blob 보존 및 실행 캐시
- HTTP transport, OAuth, 네트워크 probe

`project_records`는 record write 계약을 확정한 뒤 두 번째 수직 절단에서 추가한다.
장기 설계의 P1 우선순위를 취소하는 것이 아니라, 첫 릴리스의 검증 가능한 경계를
정하는 것이다.

## 3. workspace와 신뢰 경계

- workspace root는 저장소 manifest가 아니라 호스트의 로컬 설정 또는 시작 인자로
  제공한다.
- 요청 경로는 root에 대해 해석한 뒤 실제 경로를 다시 검사한다.
- 문자열 prefix 비교만으로 root 내부라고 판정하지 않는다.
- root 밖을 가리키는 symlink 또는 junction은 기본적으로 조회를 거부한다.
- catalog의 `trusted_for_run`은 MVP에서 정보로만 읽으며 권한을 부여하지 않는다.
- 조회 tool은 원본 파일을 수정하지 않는다. 색인과 임시 상태는 지정된 RepoPlane
  데이터 디렉터리에만 쓴다.

## 4. 공통 요청 제한

모든 목록·검색 요청은 다음 제한을 사용한다.

```json
{
  "item_limit": 50,
  "byte_limit": 65536,
  "time_limit_ms": 5000,
  "cursor": null
}
```

초기 기본값과 상한:

| 항목 | 기본값 | 상한 |
|---|---:|---:|
| `item_limit` | 50 | 500 |
| `byte_limit` | 64 KiB | 1 MiB |
| `time_limit_ms` | 5,000 | 30,000 |
| 단일 원문 range | 64 KiB | 1 MiB |
| JSONL 단일 record | 1 MiB | 8 MiB |

상한은 설정으로 더 낮출 수 있다. 상한 초과 요청을 조용히 낮추지 않고 validation
오류로 반환한다.

## 5. 공통 응답 계약

`items`는 항상 배열이다. 알 수 없는 값을 `0`, 빈 문자열, 빈 배열로 대체하지 않는다.

```json
{
  "status": "ok",
  "items": [],
  "counts": {
    "matched": 0,
    "relation": "exact",
    "returned": 0
  },
  "scan": {
    "state": "complete",
    "scope_ref": "scope:..."
  },
  "truncated": false,
  "next_cursor": null,
  "snapshot_ref": "snapshot:...",
  "warnings": []
}
```

고정 enum:

- `status`: `ok`, `partial`, `error`, `unsupported`
- `counts.relation`: `exact`, `lower_bound`, `unknown`, `not_applicable`
- `scan.state`: `complete`, `partial`, `not_applicable`
- `basis`: `declared`, `observed`, `derived`, `heuristic`, `llm_proposed`
- `validity`: `current`, `stale`, `unknown`

규칙:

- 제한 시간 전에 선언 scope를 모두 처리했을 때만 `scan.state=complete`이다.
- 중단 전 N건을 발견했으면 `matched=N`, `relation=lower_bound`로 반환할 수 있다.
- 아직 발견하지 못한 결과가 있는지조차 알 수 없으면 `truncated=null`이다.
- 일부 파일의 접근·decode·검색이 실패하면 성공 항목이 있어도 `status=partial`이다.
- warning은 안정된 machine code와 짧은 message, 관련 ref로 구성한다.

## 6. snapshot과 cursor

- 목록 검색을 완료하거나 중단한 시점의 **고정 결과 ID 목록**을 페이지화한다.
- cursor는 query hash, 결과 set ID, 다음 offset, 만료 시각에 인증된 형태로 연결한다.
- cursor 문자열 자체에 workspace 경로나 질의를 평문으로 넣지 않는다.
- 기본 TTL은 30분이며 서버 재시작 후 복구는 MVP에서 보장하지 않는다.
- `data_query`는 최초 조회의 원문 hash를 결과 set에 묶는다. 다음 페이지에서 현재
  hash가 다르거나 원문이 사라졌으면 고정 payload로 대체하지 않고
  `source_changed`를 반환한다.
- mutable path를 immutable snapshot이라고 표현하지 않는다.

## 7. Tool별 최소 계약

### 7.1 `catalog_query`

검색 대상은 `id`, `summary`, `use_when`, `tags`, `aliases`다. exact ID match를 가장
먼저 반환하고, 나머지는 고정된 lexical 점수와 `id` 오름차순으로 정렬한다.

`audit`는 설정된 후보 경로만 검사하고 다음을 구분한다.

- `unregistered_candidate`
- `missing_source`
- `duplicate_id`
- `invalid_manifest`
- `needs_review`
- `unsupported_manifest`

후보는 재사용 가능한 도구로 확정하지 않으며 `basis=heuristic`을 사용한다.

### 7.2 `path_explain`

최소 반환 항목:

- workspace ID와 workspace-relative path
- lexical path와 resolved real path
- 존재 여부와 file kind
- Git repository/worktree identity 또는 `not_applicable`
- tracked, untracked, ignored 또는 unknown
- symlink/junction/hardlink 관찰 결과와 지원 여부
- BOM, newline 분포, 마지막 newline
- 요청된 codec의 decode 및 byte round-trip 결과
- 적용 가능한 규칙 파일의 위치와 원문 ref

encoding 이름을 자동 확정하지 않는다. BOM 없음과 UTF-8 decode 성공은 각각
관찰 사실로 반환한다.

### 7.3 `workspace_search`

요청은 root, include/exclude, hidden/ignored/generated/vendor 정책, case sensitivity와
검색 mode를 명시한다. regex engine은 사용 중인 ripgrep 버전과 함께 반환한다.

결과 한 건의 단위는 다음과 같다.

- `filename`: file
- `exact`, `regex`: line match

같은 line의 복수 match를 한 건으로 셀지 여부를 schema에 고정한다. MVP에서는
`line match 1건`으로 센다.

### 7.4 `data_query`

`text_range`는 line range 또는 byte range 중 하나만 받는다. byte range는
zero-based `[byte_start, byte_end)`이고 원문 경계 밖 요청은 거부한다. line 결과는
고정 결과 set으로 페이지화한다. 단일 byte-range 항목 자체가 `byte_limit` 응답에
들어가지 않으면 조용히 자르지 않고 `limit_exceeded`로 더 좁은 range를 요구한다.

`jsonl`은 다음 순서로 처리한다.

```text
decode → record parse → equality filter → field projection → pagination
```

초기 dialect는 `jsonl-simple` 하나이며 filter는 top-level field의
문자열·boolean·null·정수 equality만 지원한다.
숫자는 `float64`로 변환하지 않고 원래 JSON token의 정밀도를 보존한다. malformed
record 정책은 `fail` 또는 `skip_with_warning` 중 요청에서 명시한다.

## 8. 오류 코드

최소 안정 오류 코드:

- `invalid_argument`
- `unsupported_operation`
- `workspace_escape`
- `source_not_found`
- `source_changed`
- `decode_failed`
- `limit_exceeded`
- `deadline_exceeded`
- `cursor_invalid`
- `cursor_expired`
- `manifest_invalid`
- `backend_unavailable`
- `internal_error`

내부 오류는 stack trace, 환경변수, 절대 사용자 경로를 MCP 응답에 노출하지 않는다.

## 9. MVP 완료 조건

- 네 tool이 stdio MCP client와 schema negotiation 및 호출에 성공한다.
- 빈 workspace가 정상 빈 결과를 반환한다.
- 깨진 manifest와 미등록 후보가 구분된다.
- scope 일부 실패가 `partial`로 보존된다.
- 제한 중단 시 count relation이 거짓 exact가 되지 않는다.
- 고정 결과의 모든 페이지를 읽었을 때 중복과 누락이 없다.
- 페이지 사이 원문 변경을 `source_changed`로 검출한다.
- UTF-8, BOM, CP949, EUC-KR, 혼합 newline fixture가 손실 없이 처리된다.
- JSONL의 큰 정수 ID가 정밀도를 잃지 않는다.
- root 밖 symlink/junction을 통한 조회가 거부된다.
- stdout에는 MCP protocol frame만 기록되고 진단 로그는 stderr로 분리된다.
