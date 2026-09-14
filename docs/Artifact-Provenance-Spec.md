# RepoPlane Artifact and Run Receipt Specification

상태: Accepted 1.1

## 1. 실행 영수증

등록된 Runner 실행은 준비, 시작, 성공, 실패, 취소, 중단 모두 durable run record로
남긴다. 최소 payload는 capability/revision, configuration, 실제 argv/cwd, executable
identity, 선언 입력 hash, preflight ref, 시작·종료 시각, exit code, termination reason,
출력 coverage와 알려진 관찰 한계를 포함한다. Runner 밖의 실행은 관찰했다고 주장하지
않는다.

환경, run, artifact record는 `records.db`에 저장하고
`project_records(kind=environment|run|artifact)`로 조회한다. 기존 record kind와 DB는
additive하게 유지한다. 서버 시작 시 종료되지 않은 `running` record는 `interrupted`로
조정하며 성공으로 추측하지 않는다.

## 2. 출력과 artifact 정책

- stdout/stderr는 stream별 identity와 offset을 유지하며 기본 최대 8 MiB씩 로컬
  artifact directory에 저장한다. 초과분은 버리고 `truncated=true`를 기록한다.
- MCP 응답은 요청된 bounded range만 반환한다. raw stream을 record payload에 넣지 않는다.
- 선언 output은 실행 전후 존재와 content hash를 snapshot하고, 변경 artifact에는 크기를
  기록한다. cache 관찰 대상은 동일 output도 capture하여 on/off 비교 증거를 보존한다.
- 기본 artifact mode는 `metadata`: byte를 복제하지 않는다.
- manifest가 `capture`를 선언한 output만 content-addressed storage에 복제한다. 기본
  artifact당 64 MiB, run당 512 MiB 한도를 적용하고 초과는 partial로 기록한다.
- 저장 위치는 workspace 밖의 state directory이며 workspace ID가 일치해야 조회할 수 있다.
- 저장된 artifact byte는 `run_inspect(action=artifact, artifact_ref=...)`로만 bounded 조회하고
  조회 때 hash를 다시 확인한다. 누락과 변조는 각각 `missing`, `corrupt`로 반환한다.

Runner v1은 raw stdout/stderr 또는 captured artifact의 내용을 자동 redaction하지 않는다.
환경 probe는 값 자체를 저장하지 않으며, 민감 output은 `metadata` mode를 사용한다. MCP로
개별 blob 삭제나 redaction을 요청하는 surface는 제공하지 않고 host가 state directory의
수명과 접근 권한을 관리한다. retention 삭제는 `deleted`로 기록하며 `redacted` storage
값은 향후 host 관리 작업의 결과를 손실 없이 표현하기 위해 예약한다.

기본 retention은 14일과 최근 200 runs 중 더 넓은 범위를 보존한다. checkpoint,
verification 또는 다른 current non-server record가 evidence/run ref로 참조한 run과
artifact는 자동 정리하지 않는다. server가 만든 순환 provenance ref만으로 stream 수명을
무한 연장하지 않는다. 정리는 한 번에 최대 64개만 처리하고 실패해도 실행 결과를
실패로 바꾸지 않는다. redacted/삭제/미보존/부분 보존을 서로 다른 상태로 기록한다.
active cache entry가 참조하는 content hash도 pin이며 entry 만료 전에는 blob을 정리하지
않는다. 재사용 run은 새 artifact record를 만들고 `basis=reused`,
`writer_attribution=cache`, source artifact evidence를 기록한다.

## 3. 완료 조건

- 실패·timeout·cancel·server restart에도 partial receipt가 남는다.
- stream별 pagination과 truncation이 정확하고 stdout/stderr 순서를 합성하지 않는다.
- 동일 content는 같은 content ref를 사용하되 hash 존재와 byte 보존을 구분한다.
- output content 관찰과 writer attribution을 분리하고, 공용 worktree의 writer는
  `unknown`으로 보존한다.
- corruption, missing blob, retention, reference 보호, 용량 한도를 시험한다.
