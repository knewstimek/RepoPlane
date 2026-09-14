# RepoPlane HTTP and Security Implementation Plan

상태: Complete
대상: [HTTP and Security Specification](HTTP-Security-Spec.md)

## Goal

stdio 호환성을 유지하면서 한 workspace용 Streamable HTTP deployment를 추가한다. 로컬 token과
외부 OAuth resource-server 연동 모두에서 인증, scope, audit와 limits가 tool handler보다 먼저
적용되도록 한다.

## 실행 보드

| 순서 | 작업 | 완료 gate | 상태 |
|---:|---|---|---|
| 1 | profile/transport 계약 | stdio default, unsafe bind 거부 | 완료 |
| 2 | auth domain과 local token | secret redaction, scope matrix | 완료 |
| 3 | OAuth introspection/metadata | audience/expiry/HTTPS/error tests | 완료 |
| 4 | Streamable HTTP wiring | current/previous SDK E2E, cancellation | 완료 |
| 5 | audit와 limits | fail-closed admission, bounded retention | 완료 |
| 6 | 보안·호환·배포 | race/full/public checks, docs/config example | 완료 |

HTTP 구현은 `net/http` middleware와 official MCP SDK handler를 조합한다. application service는
transport나 SQL concrete adapter에 직접 의존하지 않고 설정에서 조립된 interface를 받는다.
