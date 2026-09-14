# ADR-0006: HTTP mode is a scoped MCP resource server

상태: Accepted

## Context

stdio의 process boundary는 로컬 host가 권한을 제공하지만 HTTP는 network principal, workspace,
tool mutation과 denial-of-service 경계를 명시해야 한다. 자체 OAuth server를 함께 만들면 token
발급과 사용자 관리가 RepoPlane의 범위를 크게 벗어난다.

## Decision

stdio를 기본으로 보존하고 HTTP를 opt-in한다. HTTP process는 한 resource URI와 한 workspace를
제공하는 OAuth resource server다. local loopback token 및 외부 RFC 7662 introspection adapter를
제공하고 tool별 scope, Origin/Host/TLS, audit와 bounded limits를 middleware에서 강제한다.
RepoPlane은 access/refresh token을 발급하거나 다른 service로 전달하지 않는다.

## Consequences

로컬 client 호환성과 표준 OAuth deployment를 모두 지원하면서 credential lifecycle은 host
Authorization Server에 남는다. 다중 workspace routing은 process/endpoint 분리로 수행하며
나중에 암묵적인 request-root 선택으로 확장하지 않는다.

