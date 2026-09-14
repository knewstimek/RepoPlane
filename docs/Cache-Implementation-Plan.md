# RepoPlane Cache Implementation Plan

상태: Complete
대상: [Cache Specification](Cache-Spec.md)

## Goal

원설계의 보수적 정확성 원칙을 유지하면서, 검증된 순수 변환의 결과를 Runner 세 tool
안에서 재사용한다. 외부 계약은 additive하게 확장하고 기존 manifest, `run-receipt.v1`,
두 SQLite 파일과 cache-disabled 동작을 그대로 읽는다.

## 실행 보드

| 순서 | 작업 | 완료 gate | 상태 |
|---:|---|---|---|
| 1 | MCP schema 중복/크기 audit | 의미 손실 없는 축약만 채택, 직렬화 예산 test | 완료 |
| 2 | manifest와 public schema | strict decode, 기존 manifest 호환 | 완료 |
| 3 | cache domain/store와 migration | adapter test, bounded GC, pin 조회 | 완료 |
| 4 | eligibility와 HMAC key | mutation/privacy/qualification test | 완료 |
| 5 | observe와 false-hit quarantine | 동일 결과 수렴, 불일치 격리 test | 완료 |
| 6 | verified lookup/materialization | on/off byte 비교, conflict/corruption test | 완료 |
| 7 | receipt/inspect/recovery | v1 보존, v2/reused/partial test | 완료 |
| 8 | 통합·문서·배포 | full verify, 공개 검사, commit/push, binary 교체 | 진행 |

schema audit는 실제 MCP list-tools 직렬화를 기준으로 한다. 서로 다른 tool schema 사이에는
공유 `$defs` scope가 없으므로 외부 resolver를 요구하는 ref나 output schema 삭제는 축약으로
채택하지 않는다. description 중복, 잘못된 nullable, 같은 schema 내부의 반복처럼 계약을
유지하거나 강화하는 경우에만 수정한다.

초기 production catalog entry는 cache-disabled를 유지한다. 순수 fixture에서 observe 두 번,
검증 evidence, verified miss publish, verified hit materialization을 순서대로 증명한 뒤 stage를
완료한다.
