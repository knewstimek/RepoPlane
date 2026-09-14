# RepoPlane MCP Context Efficiency Implementation Plan

이 계획은 11-tool 1.0 기준선으로 완료·동결됐다. 이후 runtime 승인/configuration과 portable memory tool을
포함한 현재 계약과 예산은 [Runtime Access 구현 계획](Runtime-Access-Implementation-Plan.md),
[Runtime Configuration 명세](Runtime-Configuration-Spec.md), [Memory Backup 명세](Memory-Backup-Spec.md), [Context Efficiency 명세](Context-Efficiency-Spec.md)
1.1을 따른다.

상태: Complete
대상: [Context Efficiency Specification](Context-Efficiency-Spec.md)

## Goal

11개 직접 typed tool과 Stage 7/8 계약을 유지하면서 측정값을 분리하고, 확인 가능한 반복
응답만 opt-in 방식으로 줄인다. 유료 model 비교 없이 결정론적 로컬 증거로 이번 slice를
완료한다.

## 실행 보드

| 순서 | 작업 | 완료 gate | 상태 |
|---:|---|---|---|
| 1 | 측정 경계 확정 | bytes와 실제 model token을 구분 | 완료 |
| 2 | 계약 source 통합 | 11개 이름·scope의 fail-closed test | 완료 |
| 3 | record compact view | 기본 full 호환, exact projection, receipt | 완료 |
| 4 | 생성 증거 | schema와 group별 footprint drift test | 완료 |
| 5 | 문서·품질·배포 | focused/full/public verify, 설치 교체 | 완료 |

## 의도적으로 보류한 실험

- client native deferred exposure의 실제 token 측정과 Sol/Astra 비교
- 쓰기·Runner를 범용 toolbox로 이전
- search result grouping과 공개 error envelope 변경

이 항목은 미완성 필수 기능이 아니다. 실제 비용 증거, 호환 설계와 별도 승인·예산이 생길
때 독립 실험으로 평가한다.
