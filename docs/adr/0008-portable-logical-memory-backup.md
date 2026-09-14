# ADR-0008: Use logical portable archives for durable memory

상태: Accepted  
날짜: 2026-09-15

## Context

`records.db`와 Runner evidence는 재생성할 수 없지만 SQLite 파일 전체 복사는 열린 서버에서
일관성을 보장하기 어렵고 canonical absolute workspace identity에 묶인다. state directory
전체에는 cursor/cache/audit key와 HTTP 운영 상태도 있어 일반 memory backup에 섞으면 secret
노출과 불필요한 machine binding이 생긴다.

## Decision

RepoPlane은 record current row, 모든 revision, report-import receipt와 retained Runner regular
file만 versioned logical ZIP으로 export한다. Runtime export는 exact external destination approval과
Runner snapshot guard를 요구한다. Restore는 empty target에서 archive 계약을 검증하고 record
ownership을 현재 workspace identity로 rebind한다. Host profiles, tokens, keys, audit와 regenerable
cache는 제외한다.

## Consequences

- repository가 다른 absolute path로 복원돼도 durable semantic memory가 조회된다.
- archive는 secret bundle이 아니므로 host authentication/profile은 별도 encrypted store에서
  관리해야 한다.
- 향후 archive format 변경은 versioned importer와 migration을 요구한다.
- raw database backup보다 adapter 구현 작업은 늘지만 서비스 계층은 SQL/file copy에 의존하지
  않고 `RecordTransfer` domain operation을 사용한다.
