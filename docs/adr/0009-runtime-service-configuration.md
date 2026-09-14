# ADR-0009: local stdio가 live service bundle을 교체한다

상태: Accepted  
날짜: 2026-09-15

## 결정

RepoPlane local stdio는 승인된 `runtime_config` proposal로 source set, workspace, state
directory와 child HTTP transport를 process 재시작 없이 바꾼다. 기존 CLI/TOML 인자는
startup pre-registration으로 유지한다.

개별 singleton을 제자리에서 부분 변경하지 않는다. 새 설정으로 root, repository handles,
cursor/cache keys와 모든 feature service를 만들고 catalog refresh/Runner recovery를 검증한 뒤
router에서 bundle을 한 번에 교체한다. 기존 요청은 read lock 아래 한 bundle만 사용한다.

HTTP는 stdio가 시작·중지하는 child transport가 될 수 있지만 remote HTTP principal은
`runtime_config`를 사용할 수 없다. HTTP tool 목록은 동일하게 유지하고 호출 시 fail closed한다.

## 이유

source마다 host 설정을 수정하고 MCP를 재시작하는 흐름은 작업 중 발견한 repository context를
즉시 사용할 수 없게 한다. workspace/state까지 일부 service만 바꾸면 cursor, record ownership,
Runner와 audit가 서로 다른 root를 보게 된다. 준비 후 일괄 교체는 이 불일치를 요청 경계에서
제거한다.

## 결과

- 설정 변경마다 exact one-time MCP elicitation이 필요하다.
- active Runner/snapshot은 교체를 막고, 실패한 준비는 현재 bundle을 보존한다.
- workspace 교체는 외부 read lease를 폐기한다.
- [ADR-0007](0007-runtime-access-leases.md)의 “transport/listen/state는 시작 시 고정” 결론은
  이 ADR로 대체된다. primary execution/write 경계 자체는 계속 selected workspace 하나다.
