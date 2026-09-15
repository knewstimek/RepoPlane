# RepoPlane Records and Verification Specification

상태: Implemented 1.3
대상 수직 절단: Records/Verification → Checkpoint/Memo/Importer

## 1. 목적과 범위

이 명세는 재생성할 수 없는 프로젝트 기록의 조회, 제한된 쓰기, revision 충돌,
verification 유효성, CI report import 계약을 정의한다. 현재 read-only MVP tool의
동작을 변경하지 않는다.

포함 범위:

- `project_records` 검색·조회
- verification checklist, result, validity
- task checkpoint
- decision, failed-attempt, limitation, optional typed host-fact memo
- 신뢰된 local CI report importer
- optimistic concurrency와 record-kind별 write 권한

제외 범위:

- Runner와 임의 process 실행
- cache reuse
- artifact 원문/blob 자동 보존
- 중앙 작업 스케줄링, claim lock, 자동 commit/stash/reset
- 대화 원문 전량 수집

이 제외 목록은 Records 수직 절단의 동결 범위다. 이후 완료된 Runner/Artifact/Cache의 현재
계약은 [Full-Implementation-Roadmap.md](Full-Implementation-Roadmap.md)와 각 기능 명세를
따른다.

## 2. 읽기와 쓰기 표면

`project_records`는 항상 조회 전용이다. 숨은 import, 수정, 실행, freshness refresh를
수행하지 않는다.

쓰기 표면은 책임별로 분리한다.

| Writer | 허용 record | 금지 사항 |
|---|---|---|
| trusted importer | imported check result와 source receipt | checkpoint·memo 작성, report 의미 추측 |
| server/runner writer | 서버가 직접 관찰한 environment/run/artifact event | 사용자 목표·실패 원인 작성 |
| intention writer | checkpoint·memo 생성/수정/supersede | run/check 관찰값 위조 |

Record 외부 tool은 조회 전용 `project_records`, approval-gated `checkpoint_write`, `memo_write`,
`check_report_import`로 고정한다. Runner가 활성화되면 별도 실행 계약의 세 tool이 server
관찰 record를 만들지만 하나의 범용 `record_write(kind, payload)`는 제공하지 않는다.
local stdio의 intention write와 report import는 첫 호출에서 독립적인 runtime 승인을
요구한다. 기존 `--enable-intention-writes`, `--enable-report-import`, `--enable-runner`는
prompt 없는 host 사전 승인으로 유지한다. HTTP에서는 기존 host opt-in과 scope가 필요하다.

## 3. 공통 record 계약

모든 durable record는 최소한 다음 필드를 가진다.

```text
record_id, kind, schema_version, project_id, workspace_id?
revision, created_at, updated_at, source, writer_class
payload, evidence_refs[], supersedes?, validity
```

- ID와 ref는 opaque하며 클라이언트가 파싱하지 않는다.
- timestamp는 정렬 보조 정보이며 인과관계나 writer identity의 증거가 아니다.
- `source`는 `observed`, `imported`, `user_asserted`, `llm_proposed`를 구분한다.
- `observed`는 server/runner writer가 직접 생성한 environment/run/artifact에만 사용한다.
  클라이언트가 관찰 record를 읽고 작성한 checkpoint는 `user_asserted`이며 원 관찰은
  `evidence_refs`로 연결한다. Intention writer가 `observed`를 주장할 수는 없다.
- `workspace_id`가 없는 project-level 선언과 특정 worktree 관찰을 혼합하지 않는다.
- 삭제 대신 필요한 종류에서 tombstone/superseded 상태를 사용한다. 물리 삭제는 보존
  정책과 감사 계약이 생긴 뒤 별도 명세로 다룬다.

## 4. 조회 계약

`project_records`는 `mode=search|get|list`와 고정 snapshot cursor를 제공한다. `search`는
공백으로 분리한 최대 16개 lexical term과 512-byte 이하의 `query`를 요구하며 record ID,
kind, schema version, source와 payload의 문자열 값에서 하나 이상 일치하는 record를 찾는다.
JSON field 이름 자체는 검색 대상이 아니다. ID/metadata 일치가 payload 일치보다 우선하고,
동점은 최신 수정 시각과 ID로 안정적으로 정렬한다. filter는
최소한 `kind`, `validity`, `source`, `updated_after`를 지원한다. `project_id`와
`workspace_id`는 요청에서 받지 않고 host가 연 서버의 고정 scope를 사용한다.
지원 kind는 verification/checkpoint/memo에 environment/run/artifact를 additive하게
포함한다.

응답은 기존 공통 envelope를 재사용하며 다음을 지킨다.

- mutable query를 page마다 재실행하지 않는다.
- payload와 evidence가 크면 summary와 ref만 반환한다.
- 접근할 수 없는 record의 존재를 count나 오류 상세로 누출하지 않는다.
- current, stale, unknown, superseded, redacted, source_missing을 구분한다.
- 조회가 freshness 계산을 위해 외부 command나 network probe를 실행하지 않는다.

`search`에서 `payload_fields`를 생략하면 record kind별 발견용 field만 반환한다. Memo는
`memo_kind`, `scope`, `configuration`과 저장된 content를 공백 정규화하고 320자로 자른
결정적 `preview`를 반환한다. `host_fact`는 이 필드와 함께 typed `host`를 compact하게
반환하므로 alias 검색에서 별도 조회 tool 없이 찾을 수 있다. Preview는 저장 payload를 바꾸지 않으며 이 결과는
`payload_complete=false`다. 다른 발견용 문자열도 320자, 배열은 앞의 8개 항목으로
제한한다. 선택한 record의 전체 payload는 ID를 사용한 `get`으로 읽는다.

호출자가 다른 field를 필요로 하면 `payload_fields`로 정확한 최상위 payload field를 최대
32개까지 선택할 수 있다. 명시적 투영은 요약이 아니며 저장 값을 바꾸지 않는다. `list`와
`get`에서 이를 생략하거나 빈 배열이면 기존처럼 전체 payload를 반환한다.
`payload_complete`는 전체 payload일 때만 true다. 첫 page의 검색 결과 또는 투영은 고정
result set에 저장되어 cursor-only 다음 page에서도 유지된다.

Checkpoint, memo와 report import는 `response_view=receipt`로 방금 보낸 payload의 응답
반복을 생략할 수 있다. 기본값 `full`은 기존 계약을 유지한다. receipt도 ID, revision,
validity, evidence, duplicate와 warning을 유지하며 `payload_complete=false`를 명시한다.
`duplicate`는 importer가 같은 source identity와 content를 다시 받은 경우만 true다.
Checkpoint와 memo write는 문장의 의미상 중복·모순을 판정하지 않으므로 false가 semantic
overlap 부재를 뜻하지 않는다. 기존 memo의 변경은 ID와 `expected_revision`을 사용한
`update` 또는 `supersede`로 명시한다.

## 5. Optimistic concurrency

수정과 supersede 요청은 읽은 record의 `expected_revision`을 반드시 보낸다. 저장소는
하나의 transaction에서 현재 revision과 비교하고 새 revision을 기록한다.

- 불일치: `conflict`; 현재 payload를 자동 병합하거나 덮어쓰지 않음
- 없는 대상 수정: `not_found`; 같은 내용의 새 record를 임의 생성하지 않음
- retry: caller가 현재 revision을 다시 읽고 의미를 재검토한 뒤 명시적으로 수행
- create idempotency: importer는 source identity와 content hash로 중복 import를 검출

SQLite adapter 오류는 domain sentinel error로 변환하며 서비스는 SQL에 직접 의존하지
않는다.

## 6. Verification

Checklist 선언의 최소 필드:

```text
check_id, revision, applies_to, required, capability_ref
configurations[], success_contract, invalidation_scope
```

Result의 최소 필드:

```text
check_id, checklist_revision, subject revision과 clean/dirty 관찰
configuration, status, command-level passed/failed/unknown counts
run_ref?, report_ref?, observed_at, source
```

상태는 `passed`, `failed`, `not_run`, `blocked`, `unknown`, `stale`이다. `skipped`는
passed가 아니다. exit code 0이어도 필수 report가 없거나 실제 실행 check 수가 0이면
passed가 아니다.

Validity 계산은 checklist revision, 대상 commit/dirty 관찰, configuration,
선언된 invalidation scope를 비교한다. 관련 범위를 완전히 계산할 수 없으면 `unknown`
또는 보수적인 `stale`이며 current로 승격하지 않는다. 유효성 계산 근거를 evidence로
반환한다.

## 7. Checkpoint와 memo

Checkpoint는 rollback이나 lock이 아니라 작업 상태 기록이다. 목표, baseline commit,
dirty 범위, 관련 run/check ref, 남은 검사, 다음 작업, 알려진 위험을 저장한다. 완료
상태 이름은 `checks_satisfied`, `incomplete`, `blocked`로 제한하며 `proved_correct` 같은
표현을 사용하지 않는다.

Memo kind는 `decision`, `failed_attempt`, `resolved_failure`, `limitation`과 optional
`host_fact`를 지원한다. 적용 scope/configuration, 내용, evidence, 작성 source,
invalidation 조건을 가진다.
실패 관찰과 원인 해석을 분리하고 LLM의 원인 제안은 `llm_proposed`로 기록한다.

일반 memo는 optional `topic_key`를 사용할 수 있다. 서버는 의미 유사도를 추론하지 않으며
caller가 고른 key를 소문자로 정규화한다. Keyed memo는 `memo.v3`이고
`(scope, configuration, topic_key)`가 immutable identity다. 같은 identity의 current memo는
SQLite unique index로 경합 상황에서도 한 건만 허용한다. 동일 identity create는 새 record를
쓰지 않고 `partial`, 기존 record ref와 `memo_topic_exists` warning을 반환한다. `duplicate`는
기존대로 importer idempotency만 뜻한다. 본문·kind·invalidation condition은 같은 ID와 revision으로
update할 수 있지만 identity 변경은 저장 adapter에서도 거부한다. Rename은 기존 record를
supersede한 뒤 새 identity를 create한다.

Keyed memo create 시 같은 scope/configuration의 다른 current topic을 최근 순서로 최대 3건만
`memo_topic_related` warning과 record ref로 반환한다. Caller는 필요한 ref만 `get`한다. 이 힌트는
중복·충돌의 의미 판단이 아니며 topic 전체 목록이나 본문을 prompt에 펼치지 않는다. `topic_key`가
없는 `memo.v1` 동작은 바뀌지 않고 `host_fact`는 alias identity를 사용하므로 topic_key를 받지 않는다.

`host_fact`는 `memo.v2`이며 host alias, role, OS, production/test/staging/development tier,
실행 service 목록, 관리 path 목록과 RFC3339 `confirmed_at`을 typed `host`에 저장한다. Alias는
소문자로 정규화한다. record의 `created_at`은 서버 write 시각이지만 확인 시각을 대신하지
않으므로 `confirmed_at`은 caller가 명시한다. intention writer만 사용하므로 source는
`user_asserted` 또는 `llm_proposed`이고 server 전용 `observed`를 주장할 수 없다.

같은 alias의 current host fact가 OS 또는 role에서 다르면 write는 `host_fact_conflict`를
경고하지만 자동 병합·덮어쓰기를 하지 않는다. 같은 typed fact는 ID/revision으로 update하고,
낡은 사실은 supersede한다. `memo.v1`, `memo.v2`, `memo.v3` 사이 update 변환은 금지하며 kind를 바꿀
때는 기존 record supersede와 새 record create를 분리한다. Invalidation 문장은 저장되지만
외부 host를 자동 probe하지 않으므로 조건이 성립했을 때 caller가 명시적으로 갱신한다.

## 8. CI report importer

Importer는 host가 명시적으로 지정한 workspace-relative report만 읽는다. symlink와
junction을 포함한 실제 경로 경계를 확인하며 network에서 report를 가져오지 않는다.

초기 입력 형식은 `check-report.v1`이며 `verification-check.v1` 선언과 configuration을
함께 요구한다. Git workspace에서는 checklist가 tracked 상태여야 한다. importer는 다음
provenance를 기록한다.

- report content hash와 schema version
- parser ID/revision
- import 시각과 source kind
- subject revision/workspace fingerprint
- 원문 보존 여부(`stored`, `hash_only`, `redacted`)

v1 report가 dirty 여부만 제공하고 dirty content fingerprint를 제공하지 않으면 같은
commit과 dirty 상태가 반복돼도 `current`로 판정하지 않고 `unknown`으로 유지한다.

크기, record 수, parse time을 제한한다. 일부만 읽은 report는 partial로 남기고 전체
성공으로 처리하지 않는다. 같은 source identity와 hash의 재import는 같은 논리 결과를
반환하며 중복 result를 만들지 않는다.

## 9. 저장과 복구 경계

Durable record는 재생성 index/cache와 별도 SQLite file에 저장한다. migration,
backup/restore, corruption failure도 cache와 분리한다. 원문 blob 저장은 이 단계의
기본값이 아니며 report는 `hash_only`가 가능하다.

서비스는 `internal/store`의 작은 domain interface에 의존한다. 현재 interface는
`RecordReader`, `CheckpointWriter`, `MemoWriter`, `ReportImporter`로 분리하며 전체
repository는 조립 지점에서만 사용한다. Runner가 생길 때 관찰 event writer를 별도로
추가한다.

## 10. 보안과 정보 최소화

- record 응답에 host 절대 경로, 환경변수 값, credential을 넣지 않는다.
- writer 권한은 workspace 접근 권한에서 자동 파생하지 않는다.
- record ID 보유는 read/write 권한을 부여하지 않는다.
- redacted view와 원문 저장 여부를 별도 필드로 표현한다.
- stdout/stderr와 report 원문은 명시적 보존 정책 전에는 자동 저장하지 않는다.
- importer parse 오류에 민감한 원문 조각을 포함하지 않는다.

## 11. 구현 완료 조건

- public schema와 golden/contract test
- domain interface와 SQLite conformance test
- concurrent update conflict와 idempotent import test
- stale/unknown/skipped/report-missing validity test
- unauthorized kind write와 cross-workspace access rejection test
- crash 중 durable transaction 복구와 cache 삭제 독립성 test
- oversized/partial/redacted report test
- lexical record search의 값-only 일치, 안정적 순위, compact 기본 view와 명시적 projection test
- 기존 네 MVP tool의 read-only 회귀 test
