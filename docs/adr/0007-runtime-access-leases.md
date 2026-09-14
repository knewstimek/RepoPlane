# ADR-0007: Local stdio 권한은 runtime lease로 확장한다

상태: Accepted  
날짜: 2026-09-15

## 결정

RepoPlane은 local stdio에서 mutation, report import, Runner, cache와 primary workspace 밖의
추가 read scope를 MCP elicitation으로 승인하는 비영속 runtime lease를 제공한다. 기존
시작 플래그는 사전 승인 호환 경로로 유지한다.

외부 read lease는 조회 resolver에만 적용한다. catalog/import/Runner/cache materialization은
별도의 immutable primary resolver를 사용한다. HTTP는 bearer/OAuth와 host configuration이
권한 주체이므로 stdio runtime lease를 공유하지 않는다.

## 이유

고정 시작 플래그만 사용하면 에이전트가 작업 중 발견한 요구사항을 같은 세션에서 복구할
수 없다. 반대로 LLM이 호출할 수 있는 평범한 mutation tool이 즉시 자신에게 권한을
부여하면 사용자 승인과 모델 선택을 구분할 수 없다. MCP multi round-trip elicitation과
server-issued one-time state는 현재 호출을 유지하면서 두 주체를 분리한다.

## 결과

- 사용자는 기능별 TOML 편집과 서버 재시작 없이 작업을 계속할 수 있다.
- 이 결정 시점의 tool set은 12개로 안정화되고 `runtime_access`가 상태와 회수를 제공한다.
  이후 portable memory tool 추가 결정은 [ADR-0008](0008-portable-logical-memory-backup.md)을
  따른다.
- elicitation을 지원하지 않는 client는 runtime 확장을 완료할 수 없으며 기존 사전 승인
  플래그를 호환 경로로 사용할 수 있다.
- transport/listen/state 저장소처럼 프로세스 구성을 바꾸는 설정은 계속 시작 시점에
  고정한다.
