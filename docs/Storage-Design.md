# RepoPlane 저장 구조와 persistence interface

상태: Implemented 0.3

## 1. 저장 계층

RepoPlane은 세 종류의 데이터를 구분한다.

| 종류 | 저장소 | 성격 |
|---|---|---|
| catalog/check 선언 | workspace 일반 파일 | 사용자가 관리하는 원본 |
| 검색 색인·고정 결과·Runner cache entry | SQLite | 삭제 후 재생성 가능한 cache |
| verification/checkpoint/memo | 별도 `records.db` | 정책에 따라 보존하는 durable record |
| run/artifact | durable SQLite + content-addressed blob directory | 실행·재사용 evidence |

SQLite 파일과 임시 파일은 workspace 안에 숨겨서 만들지 않고 호스트가 지정한 로컬
data directory에 둔다. `repoplane.db`는 재생성 가능하고 `records.db`는 그렇지 않다.

SQLite는 최초 adapter이지 서비스 계층의 계약이 아니다. 서비스는
`internal/store`의 domain interface에만 의존하고 SQLite 구현은
`internal/store/sqlite`에 둔다. 다른 DB 구현은 같은 conformance test를 통과해야
한다.

## 2. Persistence interface 원칙

- 범용 `Get/Put/Delete(table, value)`나 SQL 문자열을 노출하지 않는다.
- transaction 객체를 서비스 계층에 노출하지 않는다.
- 원자성이 필요한 작업을 하나의 domain method로 표현한다.
- 저장된 JSON payload는 versioned public schema를 따라야 한다.
- adapter 고유 오류는 `store.ErrNotFound`, `store.ErrConflict` 같은 공통 오류로
  변환하되 원인은 wrapping해 진단 가능하게 한다.
- context deadline과 cancellation을 모든 DB 호출에 전달한다.
- pagination ordering은 adapter의 collation 기본값에 맡기지 않고 계약으로 고정한다.

MVP interface는 세 경계로 나눈다.

| Interface | 핵심 동작 | 원자성 계약 |
|---|---|---|
| `WorkspaceRepository` | workspace identity 조회·갱신 | 같은 ID의 upsert 한 건 |
| `CatalogRepository` | generation 발행·현재 generation 조회·검색 | 완성된 generation 전체 전환 |
| `ResultSetRepository` | 고정 결과 생성·페이지 조회·만료 정리 | result set과 ordered items 함께 생성 |

각 서비스는 가능한 한 좁은 interface만 주입받는다. 전체 `Repository`는 조립 지점에서만
사용한다. durable record 저장소도 기존 interface에 메서드를 계속 붙이지 않고
별도 `RecordReader`, `CheckpointWriter`, `MemoWriter`, `ReportImporter`로 제공한다.

## 3. Identity

- `project_id`: 명시적 로컬 설정을 우선하고, 없으면 canonical workspace 정보로 만든
  불투명 ID를 사용한다.
- `workspace_id`: canonical root와 Git worktree identity를 반영하는 불투명 ID다.
- remote URL은 identity 힌트일 뿐 권한이나 유일성의 근거가 아니다.
- 외부 응답에는 로컬 절대 경로 대신 workspace-relative path와 opaque ref를 우선한다.

ID 생성 알고리즘은 단계 1에서 version을 붙여 고정한다. 알고리즘 변경으로 기존
record를 다른 workspace에 연결하지 않는다.

## 4. MVP SQLite 논리 모델

초기 migration은 최소한 다음 관계를 가진다. 실제 column type과 index는 구현 시
versioned migration으로 고정한다.

```text
schema_migrations(version, applied_at)
workspaces(id, root_fingerprint, created_at, last_seen_at)
index_generations(id, workspace_id, source_fingerprint, created_at, state)
catalog_items(generation_id, id, revision, source_ref, execution_fingerprint, document_json)
catalog_terms(generation_id, item_id, field, term, weight)
result_sets(id, workspace_id, query_hash, generation_id, created_at, expires_at, item_count, metadata_json)
result_items(result_set_id, ordinal, item_ref, item_hash, payload_json)
cache_entries(cache_key, workspace_id, capability, state, source_run_ref, outputs_json, qualification_refs_json, timestamps, counts)
```

`schema_migrations`는 순차 migration을 기록한다. 현재 SQLite adapter는 기존
pre-release result-set shape를 보정하고, 지원 버전보다 새로운 DB는 빈 상태로
간주하지 않고 명시적으로 거부한다. `execution_fingerprint`는 실행 권한이나 artifact
보관을 뜻하지 않으며, 같은 catalog revision에서 실행 파일이 바뀐 경우
`needs_review`를 계산하기 위한 관찰값이다.

규칙:

- 새 index는 별도 generation으로 완성한 뒤 transaction에서 current pointer를 바꾼다.
- invalid manifest가 포함된 refresh는 정상 항목과 오류를 함께 새 generation에 기록하되,
  파서 자체가 실패하면 이전 current generation을 유지한다.
- pagination은 mutable query 재실행이 아니라 `result_items.ordinal`을 읽는다.
- 만료 결과 정리는 요청 처리와 분리하고 bounded batch로 수행한다.
- cache observation publish는 같은 key/output을 한 entry로 수렴시키고 다른 output은 같은
  transaction에서 `quarantined`로 바꾼다.
- active cache output hash는 artifact retention의 pin이며 cache entry 만료 정리는 한 번에
  최대 64개다.

## 5. Ref 형식

ref는 opaque string이며 다음 의미 종류를 내부적으로 갖는다.

- `source`: mutable workspace path와 관찰 hash
- `scope`: 검색 root와 include/exclude 정책
- `snapshot`: index generation 또는 고정 result set
- `cursor`: result set의 다음 위치
- `check`, `checkpoint`, `memo`
- 향후 `artifact`, `run`

클라이언트가 ref 문자열을 파싱해야만 동작하는 계약을 만들지 않는다. ref 조회 시
원본이 없거나 바뀌었으면 현재의 비슷한 파일로 대체하지 않는다.

## 6. Cursor 보호

cursor payload의 논리 필드:

```text
format_version, workspace_id, result_set_id, next_ordinal, expires_at
```

payload는 호스트 local secret으로 인증한다. secret 값은 workspace, DB, 로그, 응답에
저장하지 않는다. key가 바뀌면 기존 cursor는 명시적으로 invalid가 된다.

## 7. 동시성과 복구

- schema migration과 generation 전환은 SQLite transaction을 사용한다.
- expected revision이 필요한 durable record는 compare-and-swap 조건을 사용한다.
- 프로세스 crash 뒤 `building` generation은 current가 될 수 없다.
- DB corruption 또는 schema incompatibility를 빈 검색 결과로 바꾸지 않는다.
- 재생성 가능한 cache와 durable record는 파일 또는 DB를 분리해 복구 경계를
  명확히 한다.

## 8. Adapter conformance test

모든 adapter는 다음 공통 테스트를 통과해야 한다.

- publish 실패 중에는 이전 current generation이 유지됨
- 동시 reader가 불완전 generation을 관찰하지 않음
- exact ID, score 내림차순, ID 오름차순 정렬이 동일함
- result set ordinal의 중복·누락이 거부됨
- result set page가 원래 순서를 유지함
- context 취소가 bounded time 안에 반환됨
- 만료 정리가 요청한 batch limit을 넘지 않음
- adapter 오류가 공통 sentinel error와 `errors.Is`로 비교 가능함

SQLite 전용 migration과 query plan 테스트는 공통 conformance test와 별도로 둔다.

## 9. Durable store 경계

Records 수직 절단은 별도 `records.db` migration으로 record, record revision, report import
receipt를 추가한다. `RecordReader`, `CheckpointWriter`, `MemoWriter`, `ReportImporter`를
분리하고 수정은 expected revision compare-and-swap을 사용한다. cache DB 삭제나 재색인은
durable record에 영향을 주지 않는다.

artifact blob은 여전히 content-addressed storage 후보지만, hash가 존재한다는 사실과 byte를
보관하고 있다는 사실을 분리한다. TTL, redaction, access policy가 확정되기 전에는
민감할 수 있는 stdout/stderr나 원문 blob을 자동 보존하지 않는다.
