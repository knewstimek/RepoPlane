# ADR-0005: Search adapters preserve independent evidence classes

상태: Accepted

## Context

Git history, current files와 generated symbol index는 freshness와 completeness가 다르다. 하나의
통합 점수나 빈 결과로 합치면 정확 일치를 숨기고 adapter 부재를 검색 부재로 오인할 수 있다.

## Decision

기존 조회 tool을 확장하되 각 adapter는 basis, engine, scope, validity와 completeness를
독립적으로 공개한다. Git revision은 immutable ref로 고정하고 symbol은 기존 명시 index만
읽는다. frontmatter와 structured-data parser는 실행 없는 strict subset을 사용한다. semantic
ranking은 이번 단계에 포함하지 않는다.

## Consequences

호출자는 필요한 channel을 명시하며 서로 다른 결과를 스스로 비교할 수 있다. adapter가 없는
저장소도 기존 검색을 계속 쓰고 `unsupported`를 정확히 받는다. 범용 symbol graph의 편의보다
재현 가능한 evidence와 작은 schema를 우선한다.

