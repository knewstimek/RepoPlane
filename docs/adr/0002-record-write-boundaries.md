# ADR-0002: Record 조회와 제한된 쓰기 표면을 분리한다

상태: Accepted
날짜: 2026-09-14

## 맥락

장기 설계는 `project_records`를 조회 도구로 정의하지만 checkpoint, memo, CI import와
서버가 관찰한 실행 사건은 durable write가 필요하다. 조회 tool에 범용 write mode를
추가하면 호출 의도, 권한, retry, 감사 경계가 불분명해진다. importer만 허용하면
에이전트나 사용자의 목표와 결정처럼 외부 report에 존재하지 않는 의미 정보는 저장할
수 없다.

## 결정

- `project_records`는 조회 전용으로 유지한다.
- 서버 관찰 event, trusted import, 사용자 의도 기록은 서로 다른 writer capability로
  분리한다.
- checkpoint와 memo는 좁은 전용 write 계약으로만 생성·수정한다.
- mutable intention record의 수정과 supersede에는 `expected_revision`을 요구하고
  compare-and-swap으로 충돌을 검출한다.
- importer는 source identity와 content hash를 사용해 idempotent하게 동작한다.
- writer는 허용된 record kind만 쓸 수 있다. workspace 접근이나 record ID 보유만으로
  write 권한을 얻지 않는다.
- Runner writer는 Runner 명세가 승인될 때 추가하며 현재 Records 구현에서 미리
  실행 권한을 만들지 않는다.

## 결과

조회 호출은 계속 side-effect free이며 host가 read-only MCP만 노출할 수 있다. 의도
기록과 관찰 기록을 구분할 수 있고 record kind별 최소 권한을 적용할 수 있다. 대신
tool과 domain interface가 하나보다 많아지고 host 조립 단계에서 writer capability를
명시적으로 설정해야 한다.

## 기각한 대안

### `project_records(mode=write)`

작은 tool 수에는 유리하지만 조회와 변경의 권한 및 재시도 의미가 섞이므로 기각한다.

### Importer만 write 허용

CI 결과에는 적합하지만 checkpoint 목표, 다음 작업, decision memo를 표현할 수 없어
기각한다.

### Workspace Markdown만 사용

사람이 검토하기 쉽지만 자동 event, concurrent revision, 접근 제어와 bounded query를
일관되게 제공하지 못한다. 선언과 장기 메모에는 계속 사용할 수 있으나 유일한 durable
store로 사용하지 않는다.

## 재검토 조건

- MCP host가 표준화된 fine-grained mutation capability를 제공할 때
- HTTP 다중 사용자 배포가 writer identity 또는 감사 계약을 바꿀 때
- 실제 사용에서 checkpoint/memo 전용 surface가 과도한 호출 실패를 만들 때
