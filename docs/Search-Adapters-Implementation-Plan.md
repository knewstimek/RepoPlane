# RepoPlane Search Adapters Implementation Plan

상태: Complete
대상: [Search Adapters Specification](Search-Adapters-Spec.md)

## Goal

기존 세 조회 tool의 독립 계약을 유지하면서 Git, symbol, frontmatter, JSON/log/CSV/TSV를
bounded adapter로 추가한다. 검색 범위가 늘어나도 부재·부분·stale을 성공적인 전수 검색으로
오인하지 않는다.

## 실행 보드

| 순서 | 작업 | 완료 gate | 상태 |
|---:|---|---|---|
| 1 | public request/result와 schema 예산 | additive 호환, 11 tools, 32 KiB | 완료 |
| 2 | Git history adapter | immutable ref, shallow/partial, fixed pages | 완료 |
| 3 | symbol-index adapter | optional/unsupported, validity, dialect tests | 완료 |
| 4 | Markdown frontmatter | strict parser, atomic catalog generation | 완료 |
| 5 | JSON/log/delimited query | precision, malformed, encoding, limits | 완료 |
| 6 | 품질·문서·배포 | baseline/E2E/full verify, public check | 완료 |

adapter는 기존 `ResultSetRepository`를 재사용하고 SQL에 직접 의존하지 않는다. 실행 파일
probe는 Preflight의 현재 evidence를 재사용하며 선택 adapter 실패 때문에 서버 시작이나 기존
검색을 막지 않는다.
