# RepoPlane 문서 안내

문서마다 역할을 분리한다. 아키텍처 문서에 진행 상황이나 임시 구현 세부사항을
누적하지 않는다.

| 문서 | 역할 | 변경 시점 |
|---|---|---|
| [Project-Control-Plane-MCP-Design.md](Project-Control-Plane-MCP-Design.md) | 목표, 원칙, 장기 범위, 설계 근거 | 제품 방향이나 원칙이 바뀔 때 |
| [MVP-Spec.md](MVP-Spec.md) | 첫 구현의 지원 범위와 외부 계약 | 동작 계약이 바뀔 때 |
| [Implementation-Plan.md](Implementation-Plan.md) | 구현 순서, 완료 조건, 보류 항목 | 작업 단계가 진행되거나 재계획될 때 |
| [Full-Implementation-Roadmap.md](Full-Implementation-Roadmap.md) | 채택된 P1/P2 범위, 수직 절단 순서와 gate | 장기 구현 순서나 채택 범위가 바뀔 때 |
| [Records-Spec.md](Records-Spec.md) | durable record, verification, write/import 계약 | Records 외부·저장 계약이 바뀔 때 |
| [Preflight-Spec.md](Preflight-Spec.md) | capability 환경 점검과 hard/soft gate 계약 | preflight 관찰이나 redaction이 바뀔 때 |
| [Artifact-Provenance-Spec.md](Artifact-Provenance-Spec.md) | run receipt, stream, artifact 보존 계약 | 실행 기록이나 retention이 바뀔 때 |
| [Runner-Spec.md](Runner-Spec.md) | prepare/execute/inspect와 플랫폼 실행 계약 | Runner 권한이나 실행 동작이 바뀔 때 |
| [Cache-Spec.md](Cache-Spec.md) | cache 적격성, key, 재사용, materialization 계약 | cache 외부·저장 계약이 바뀔 때 |
| [Cache-Implementation-Plan.md](Cache-Implementation-Plan.md) | Stage 6 구현 순서와 검증 gate | cache 작업 단계가 진행되거나 재계획될 때 |
| [Search-Adapters-Spec.md](Search-Adapters-Spec.md) | Git/symbol/frontmatter/structured-data 검색 계약 | search adapter 계약이 바뀔 때 |
| [Search-Adapters-Implementation-Plan.md](Search-Adapters-Implementation-Plan.md) | Stage 7 구현 순서와 gate | search adapter 작업이 진행되거나 재계획될 때 |
| [HTTP-Security-Spec.md](HTTP-Security-Spec.md) | HTTP transport, auth, scope, audit와 limit 계약 | network/security 계약이 바뀔 때 |
| [HTTP-Implementation-Plan.md](HTTP-Implementation-Plan.md) | Stage 8 구현 순서와 gate | HTTP/Auth 작업이 진행되거나 재계획될 때 |
| [Storage-Design.md](Storage-Design.md) | SQLite, blob, ref, cursor 저장 계약 | 영속성 구조가 바뀔 때 |
| [Public-Release.md](Public-Release.md) | 공개 설명, topic, 최종 공개 체크리스트 | 릴리스 준비와 공개 시점 |
| [Verification.md](Verification.md) | 완료 조건과 실행 가능한 증거 매핑 | 완료 게이트나 검증 방식이 바뀔 때 |
| [adr/0001-use-go.md](adr/0001-use-go.md) | Go 선택 결정과 결과 | 결정을 뒤집거나 보완할 때 |
| [adr/0002-record-write-boundaries.md](adr/0002-record-write-boundaries.md) | record read/write 권한과 충돌 경계 | writer 책임이나 권한 모델이 바뀔 때 |
| [adr/0003-runner-authority-and-compatibility.md](adr/0003-runner-authority-and-compatibility.md) | Runner 권한, 실용적 gate, 호환성 결정 | 실행 승인이나 호환성 원칙이 바뀔 때 |
| [adr/0004-conservative-cache-qualification.md](adr/0004-conservative-cache-qualification.md) | cache opt-in, qualification, false-hit 결정 | cache 승인이나 격리 원칙이 바뀔 때 |
| [adr/0005-search-adapter-evidence-boundaries.md](adr/0005-search-adapter-evidence-boundaries.md) | 검색 evidence channel과 adapter 부재 경계 | 검색 근거 결합 방식이 바뀔 때 |
| [adr/0006-http-resource-server-boundary.md](adr/0006-http-resource-server-boundary.md) | HTTP resource-server와 인증 책임 경계 | HTTP 인증·workspace 경계가 바뀔 때 |

## 문서 우선순위

서로 충돌하면 다음 순서로 판단한다.

1. 버전이 명시된 machine-readable schema와 회귀 테스트
2. 해당 기능의 versioned 명세 (`MVP-Spec.md`, `Records-Spec.md` 등)
3. 승인된 ADR
4. 장기 설계문서
5. `Implementation-Plan.md`의 진행 메모

구현 중 발견한 계약 변경은 코드에만 반영하지 않는다. 먼저 MVP 명세 또는 ADR을
갱신하고, 공개 schema와 테스트를 같은 변경에 포함한다.
