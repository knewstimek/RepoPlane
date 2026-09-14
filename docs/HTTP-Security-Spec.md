# RepoPlane HTTP and Security Specification

상태: Accepted 1.1

## 1. transport와 호환성

stdio는 기본 transport이며 기존 startup과 tool 계약을 바꾸지 않는다. HTTP는 명시적
`--transport http` startup 또는 local stdio의 승인된 `runtime_config`와 ignored host
profile로 활성화한다. MCP endpoint는 official Go SDK의
Streamable HTTP를 사용하며 2026-07-28 stateless 동작을 우선하고 SDK가 지원하는 이전
protocol negotiation을 보존한다. legacy HTTP+SSE endpoint는 새로 제공하지 않는다.

각 HTTP endpoint는 한 시점에 한 trusted workspace만 제공한다. HTTP request 인자로 workspace
root를 바꾸지 않으며 remote principal의 `runtime_config`는 fail closed한다. local stdio가
bundle을 교체하면 listener와 audit store도 graceful restart된다. MCP tool 수와 schema는
transport 사이에 같다.

## 2. host profile과 listener

profile은 listen address, public resource URI, endpoint path, allowed origins/hosts, TLS mode,
authentication adapter, scopes, request/concurrency/rate limits와 audit retention을 선언한다.
secret 값은 profile에 직접 쓰지 않고 명시된 environment variable 또는 권한 제한 파일에서
읽는다. tracked 문서에는 placeholder만 둔다.

- 기본 listener는 loopback이다. wildcard/public bind는 direct TLS가 있거나 loopback의
  명시된 reverse-proxy deployment가 아니면 거부한다.
- present `Origin`은 exact allowlist로 검증하며 invalid origin은 403이다. Origin 부재는
  non-browser client를 위해 허용한다.
- Host allowlist, request-body/header 한도, read-header/idle timeout과 graceful shutdown을
  적용한다. reverse proxy는 loopback listener에 연결하고 원래 Host를 보존한다. forwarded
  header는 권한이나 resource 판정에 사용하지 않아 spoofing 경계를 만들지 않는다.
- MCP endpoint의 permissive CORS와 token query parameter를 허용하지 않는다.

## 3. authentication

모든 HTTP MCP request는 bearer authentication을 요구한다. 두 host adapter를 제공한다.

- `local_token`: loopback 개발/개인 사용. 권한 제한 파일의 고엔트로피 token과 명시 scope를
  constant-time 비교한다. OAuth 지원을 주장하거나 원격 public bind에 사용하지 않는다.
- `oauth_introspection`: 외부 Authorization Server의 고정 HTTPS RFC 7662 endpoint에 token을
  확인한다. active, expiry, resource/audience, subject와 scope를 검증한다. RepoPlane은 token을
  발급·교환·전달하지 않는다. Protected Resource Metadata는 고정 resource URI와 configured
  authorization server만 공개한다.

원문 token, client secret과 credential header는 log, audit, record, error에 기록하지 않는다.
introspection은 deadline, response byte limit, TLS, bounded connection pool을 사용한다. 실패는
인증 실패 또는 일시적 503으로 분리하며 이전 성공을 무기한 재사용하지 않는다.

## 4. authorization

resource/audience 검증은 process의 public MCP resource URI에 결합한다. tool별 최소 scope는
다음과 같다.

- 조회 5개: `repoplane.read`
- checkpoint/memo: `repoplane.intent.write`
- report import: `repoplane.report.import`
- Runner 3개와 cache reuse: `repoplane.runner.execute`
- portable memory export와 runtime configuration: `repoplane.state.export` (HTTP runtime
  grant/configuration은 지원하지 않음)

host opt-in flag와 token scope를 모두 만족해야 한다. broader scope implication은 profile에
명시하고 임의 문자열 prefix로 추론하지 않는다. 인증은 tool 목록 노출 여부와 별개이며 실제
call에서 항상 다시 검사한다. 부족한 scope는 403과 한 번의 complete scope challenge를
반환한다.

## 5. audit와 limits

HTTP admission은 별도 `AuditRepository` domain interface를 사용한다. SQLite `audit.db`는
semantic `records.db` 및 재생성 index와 분리한다. event는 request ID, HMAC pseudonymous
principal, workspace ID, method/tool, auth/authorization decision, status class, duration와
bounded byte counts만 저장한다. argument, result, token, IP와 local path는 기본 저장하지 않는다.

admission audit를 기록하지 못하면 request를 실행하지 않는다. completion 갱신 실패는 stderr
운영 오류와 incomplete event로 남긴다. 기본 30일/100,000 event를 보존하고 pass당 최대 256개를
삭제한다.

global/per-principal concurrent request, request rate, body size와 authentication upstream
concurrency를 제한한다. limiter identity map은 TTL과 최대 entry 수가 있어야 한다. 제한은
429/503과 retry information으로 표현하며 MCP tool의 자체 item/byte/time limit을 대체하지 않는다.

## 6. 완료 gate

- stdio golden regression과 official SDK HTTP client E2E
- current stateless 및 SDK-supported previous negotiation
- missing/invalid/expired/wrong-audience token과 모든 scope matrix
- Origin/Host/public-bind/TLS/trusted-proxy/body/rate/concurrency 거부
- disconnect cancellation, graceful shutdown, concurrent requests와 restart
- audit redaction, fail-closed admission, bounded retention
- token/secret/path privacy scan, race gate와 full public verification
