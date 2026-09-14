# ADR-0003: 등록 capability를 실용적인 기본값으로 실행한다

이 결정의 시작 시점 전용 local opt-in은 1.0 기준선이다. local stdio의 현재 runtime
승인 계약은 [ADR-0007](0007-runtime-access-leases.md)이 보완하며 HTTP 경계는 유지된다.

- 상태: Accepted
- 날짜: 2026-09-15

## 맥락

모든 불확실성을 차단 조건으로 만들면 일반적인 build/test도 실행하지 못하고 플랫폼별
도구 차이가 호환성 장애가 된다. 반대로 plan ID나 `trusted_for_run`만 승인으로 보면
실행 권한과 변경 시점 검사가 약해진다.

## 결정

- 실행 계층은 host의 `--enable-runner`와 MCP client의 실행 tool 승인을 권한 경계로 삼는다.
- catalog의 `trusted_for_run=true`는 실행 후보 조건이지만 그 자체가 사용자 승인은 아니다.
- preflight를 required/recommended/informational로 나누고 required만 hard gate로 사용한다.
- 별도 environment tool을 추가하지 않고 `run_prepare`와 `project_records`를 사용한다.
- 기존 manifest 필드는 그대로 해석하며 새 policy 필드는 선택적으로 추가한다.
- Windows/Linux adapter는 같은 논리 계약과 각 OS의 native process 동작을 함께 시험한다.
- 관련 없는 dirty 변경, optional probe의 unknown, capture 실패는 명시적 partial/warning으로
  보존하고 실행 성공 자체와 혼동하지 않는다.

## 결과

일반적인 등록 build/test는 중복 승인 없이 실행할 수 있고, 실제로 필요한 경계만 실행을
막는다. 세 실행 tool이라는 최초 설계의 작은 MCP 표면도 유지한다. 대신 manifest 작성자와
host가 trusted capability를 검토해야 하며 OS별 adapter test가 필수다.
