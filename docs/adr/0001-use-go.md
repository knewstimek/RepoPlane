# ADR-0001: RepoPlane 서버를 Go로 구현한다

- 상태: Accepted
- 날짜: 2026-09-14

## 맥락

RepoPlane은 MCP protocol adapter보다 filesystem 탐색, bounded streaming, hash,
SQLite, subprocess와 Windows/Linux 차이를 다루는 비중이 크다. 단일 실행 파일로
배포하고 runner 단계에서 timeout과 cancellation을 명확히 전파할 필요가 있다.

검토한 후보는 Go, Rust, TypeScript/Node.js다. 세 언어 모두 공식 MCP SDK가 있으므로
SDK 존재 여부는 결정 요인이 아니다.

## 결정

서버와 CLI를 Go로 구현한다.

- 공식 SDK `github.com/modelcontextprotocol/go-sdk/mcp`를 사용한다.
- 최초 transport는 stdio다.
- context cancellation과 deadline을 모든 backend 경계에 전달한다.
- OS별 경로·파일 identity·프로세스 처리는 작은 platform package와 build tag 뒤에 둔다.
- SQLite 선택 시 배포 형태와 CGO 사용 여부를 명시적으로 결정하고 기록한다.
- 외부 tool 호출은 argv 배열과 제한된 환경으로 수행하며 임의 shell 문자열을 받지
  않는다.

## 결과

장점:

- Windows와 Linux용 단일 binary 배포가 단순하다.
- 정적 타입으로 MCP request/response 계약을 표현할 수 있다.
- I/O 중심 workload에 충분한 성능과 비교적 낮은 구현 복잡도를 제공한다.
- 표준 `context`, `io`, `os/exec` 모델을 runner 설계에 재사용할 수 있다.

비용:

- Rust보다 compile-time aliasing/ownership 보장이 약하다.
- TypeScript보다 MCP 예제와 UI/web 생태계의 코드를 직접 재사용하기 어렵다.
- encoding, JSON Schema, SQLite driver의 구체적 선택과 호환성을 별도로 검증해야 한다.
- Windows process tree와 junction 처리는 여전히 OS 전용 구현과 회귀 테스트가 필요하다.

## 재검토 조건

다음 중 하나가 실측으로 확인될 때 언어 또는 native helper 분리를 재검토한다.

- Go 구현이 요구 메모리·지연 한도를 지속적으로 충족하지 못함
- 필수 보안 격리가 Go에서 사용할 수 없는 OS API 또는 검증된 library를 요구함
- 공식 Go MCP SDK가 필요한 protocol 기능을 지원하지 못함

성능 우려만으로 미리 Rust helper를 추가하지 않는다. 먼저 profiler와 회귀 fixture로
병목을 확인한다.
