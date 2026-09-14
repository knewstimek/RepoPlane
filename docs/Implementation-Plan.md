# RepoPlane 구현 계획

상태: Complete
대상: [MVP 명세](MVP-Spec.md) MVP 1.0

이 문서는 완료된 MVP 1.0의 실행 기록으로 동결한다. 이후 채택된 P1/P2 구현은
[Full-Implementation-Roadmap.md](Full-Implementation-Roadmap.md)에서 추적한다.

## Goal

Go로 조회 전용 RepoPlane MCP MVP를 구현한다. 공식 MCP Go SDK와 stdio transport를
사용해 `catalog_query`, `workspace_search`, `path_explain`, `data_query`를 제공하고,
MVP 명세 9절의 완료 조건과 회귀 테스트를 모두 충족한다.

이번 Goal에서 제외한다.

- Runner와 실행 cache
- `project_records`와 record 쓰기
- semantic/symbol/Git-history adapter
- HTTP transport와 인증

## 실행 보드

| 순서 | Milestone | 상태 | 통과 게이트 |
|---:|---|---|---|
| 0 | 저장소와 계약 골격 | 완료 | 재현 가능한 build/test와 stdio smoke test |
| 1 | 공통 contract와 workspace 경계 | 완료 | golden, root escape, cursor, limit test |
| 2 | Catalog | 완료 | 빈/오류/중복/감사 fixture와 결정적 정렬 |
| 3 | Workspace search | 완료 | 범위·partial·pagination 회귀 test |
| 4 | Path facts | 완료 | Windows 경로와 encoding/newline fixture |
| 5 | Data query | 완료 | bounded range, JSONL, 큰 정수 test |
| 6 | MVP 통합과 평가 | 완료 | MVP 명세 9절 전체 통과 |
| 7 | 공개 릴리스 | 완료 | 공개 전 검사, README/설명 정리, public 원격 생성·push |

상태 값은 `대기`, `진행`, `완료`, `차단`만 사용한다. 선행 단계의 통과 게이트를
충족하기 전에는 후행 단계를 완료로 표시하지 않는다. 독립적인 fixture나 문서 작업은
병렬로 준비할 수 있다.

## 실행 순서와 검증 주기

```text
0 골격
  → 1 공통 계약/경계
    → 2 Catalog
    → 3 Search
    → 4 Path Facts
    → 5 Data Query
      → 6 통합/평가
        → 7 공개 릴리스
```

각 milestone은 `구현 → package test → contract/fixture test → 문서 갱신` 순으로
닫는다. 전체 test만 마지막에 실행하지 않고 단계별 실패 범위를 작게 유지한다.

## 원칙

- 각 단계는 독립적으로 실행되는 테스트와 완료 조건을 가진다.
- 외부 계약을 바꾸는 작업은 schema와 문서를 먼저 또는 같은 변경에서 갱신한다.
- Windows를 최초 지원 플랫폼으로 두되 경로·프로세스 코드는 플랫폼 경계 뒤에 둔다.
- 경고가 많은 build/test 출력은 로컬 로그에 저장하고 최종 요약만 표시한다.
- Runner와 cache는 조회 기반의 정확성을 확보한 뒤 시작한다.

## 단계 0 — 저장소와 계약 골격

작업:

- Git 저장소 및 Go module 초기화
- `cmd/repoplane`과 `internal` package 골격
- 공식 MCP Go SDK 고정 버전 선택
- stdio MCP smoke server와 in-process client test
- lint, unit test, Windows build 명령 정의
- `schemas/`에 versioned 공개 schema 추가

완료 조건:

- 깨끗한 checkout에서 한 명령으로 build/test가 성공한다.
- stdout protocol과 stderr logging 분리를 자동 테스트한다.
- 의존성 버전과 지원 MCP protocol version이 기록된다.

## 단계 1 — 공통 contract와 workspace 경계

작업:

- status, counts, scan, warning, evidence, ref 타입
- request limit validation
- workspace root canonicalization
- snapshot result set과 cursor 서명·TTL
- bounded reader와 content hash

완료 조건:

- 공통 response golden test 통과
- root escape, 만료 cursor, source change test 통과
- limit 초과가 명시적 오류로 반환됨

## 단계 2 — Catalog

작업:

- YAML/JSON manifest parser와 schema validation
- deterministic lexical index
- search/get/list/status
- 설정된 후보 경로에 대한 bounded audit

완료 조건:

- 빈 catalog, 중복 ID, 깨진 manifest, 미등록 후보 fixture 통과
- 같은 snapshot과 query가 byte-equivalent ordering을 반환
- 잘못된 manifest가 정상 항목으로 색인되지 않고 다른 정상 항목은 보존됨

## 단계 3 — Workspace search

작업:

- 파일 열거와 include/exclude 정책
- ripgrep capability/version 확인
- filename/exact/regex backend
- partial scan과 lower-bound count

완료 조건:

- hidden/ignored/generated/vendor 정책별 fixture 통과
- timeout과 접근 실패가 정확한 coverage를 반환
- pagination 전체 결과가 baseline과 일치

## 단계 4 — Path facts

작업:

- Git repository/worktree와 tracked 상태
- realpath, symlink, junction, hardlink 관찰
- BOM, newline, codec decode/round-trip 검사
- 규칙 파일 profile과 scope 반환

완료 조건:

- Windows path fixture와 지원되는 Unix test 통과
- CP949/EUC-KR을 근거 없이 서로 확정하지 않음
- 같은 basename 또는 같은 hash만으로 canonical 역할을 만들지 않음

## 단계 5 — Data query

작업:

- bounded line/byte range
- streaming JSONL parser
- equality filter와 projection
- malformed record 정책과 큰 scalar 처리

완료 조건:

- 대형 정수와 문자열 ID가 보존됨
- 단일 oversized record가 명시적으로 잘리거나 거부됨
- malformed record의 skip/fail 의미가 테스트됨

## 단계 6 — MVP 통합과 평가

작업:

- 네 MCP tool 통합
- crash-safe cache 재생성
- end-to-end fixture workspace
- Markdown + rg baseline과 정확도·호출량 비교
- 배포용 Windows/Linux binary build

완료 조건:

- `MVP-Spec.md` 9절을 전부 통과
- 알려진 security/coverage 실패를 문서화
- 설치·설정·제거 절차가 재현 가능

## 단계 7 — 공개 릴리스

MVP 통합과 전체 검증이 끝난 뒤에만 수행한다. 그전에는 외부 저장소를 만들거나
코드를 공개하지 않는다.

작업:

- README를 설치, 사용 예시, 핵심 가치, 제한사항, roadmap 중심으로 최종 편집
- GitHub용 짧은 project description과 topic 선정
- 비밀, credential, 로컬 절대 경로, DB, key, 로그, 임시 산출물 검사
- license, contribution guide, security policy, issue template 검토
- public GitHub repository 생성과 최초 push
- fresh clone에서 build/test 재검증

완료 조건:

- MVP 단계 6이 먼저 완료됨
- 공개 전 검사 결과에 미해결 비밀 또는 로컬 artifact가 없음
- public 원격의 기본 branch에서 README와 CI가 정상 표시됨
- fresh clone의 전체 검증이 통과함

## 단계 8 — MVP 종료 시 다음 수직 절단 전 결정

Records의 다음 결정은 이후 [ADR-0002](adr/0002-record-write-boundaries.md)와
[Records-Spec.md](Records-Spec.md)에서 확정하고 구현했다.

해결된 Records 결정:

- `project_records`를 조회 전용으로 유지할지
- checkpoint/memo/import를 기록하는 별도 tool 또는 명시적 mode를 둘지
- server event와 사용자 의도 record의 revision 충돌 처리

후속 수직 절단을 구현하기 전에 아직 확정할 결정:

- artifact blob 보존 기간과 redaction 권한 모델
- runner 승인 정책의 저장 위치와 호스트 연동 방식

장기 설계는 `project_records`를 읽기 전용으로 설명하면서 task intention의 명시적
저장도 요구한다. 이 write surface를 정하기 전에는 이름을 추측해 구현하지 않는다.

## MVP 종료 시 보류 기능

이 목록은 MVP 동결 시점의 상태를 기록한다. 현재 상태는
[Full Implementation Roadmap](Full-Implementation-Roadmap.md)을 기준으로 한다.

MVP 이후 완료:

- `project_records`, verification, checkpoint/memo/importer
- 환경 preflight
- 실행 영수증과 artifact provenance
- `run_prepare`, `run_execute`, `run_inspect`

MVP 동결 뒤 완료(현재 상태는 active roadmap 기준):

- cache eligibility와 artifact 재사용
- semantic/symbol/Git-history adapter
- HTTP transport와 인증

위 목록은 원 MVP 범위에서는 제외였다는 역사적 기록이다. 실제 지원 계약과 완료 상태는
active roadmap 및 각 후속 명세를 따른다. semantic ranking은 후속 명세에서도 선택 범위다.
