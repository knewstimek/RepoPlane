# ADR-0004: Cache reuse requires host opt-in and current qualification

상태: Accepted

## Context

Runner receipt와 artifact CAS는 결과 byte를 보존하지만, 같은 결과를 다시 써도 안전하다는
증거는 아니다. 지나치게 넓은 worktree fingerprint는 유용한 hit를 없애고, 선언되지 않은
환경이나 side effect를 무시하면 false hit가 된다.

## Decision

Cache는 별도 `--enable-cache` host opt-in과 manifest policy를 모두 요구한다. key는 선언된
dependency만 포함하며 host-local HMAC으로 저장한다. `observe`는 실행 결과를 비교하지만
자동 승격하지 않는다. `verified` 재사용은 versioned manifest/cache contract와 현재의
qualification verification을 함께 요구한다. cache 거절은 Runner 실행 거절이 아니다.

새 tool이나 범용 gateway를 만들지 않는다. 재사용은 Runner 세 tool의 additive field로
표현하고 `run-receipt.v2`가 subprocess 실행과 materialized reuse를 구분한다.

## Consequences

cache 대상 작성자는 inputs, outputs, runtime identity와 purity assumption을 완전하게 선언해야
한다. 일반 build cache가 더 정확하면 그것을 우선한다. index 삭제는 hit율만 낮추며 durable
record를 손상하지 않는다. false-hit는 entry quarantine과 contract revision 변경 및 재검증을
요구하므로 조용히 계속 재사용되지 않는다.

