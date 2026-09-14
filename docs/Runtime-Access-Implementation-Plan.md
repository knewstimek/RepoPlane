# RepoPlane Runtime Access Implementation Plan

상태: Complete  
기준선: [Runtime Access 명세](Runtime-Access-Spec.md)

## 목표

local stdio 사용자가 작업 중 권한 부족을 발견했을 때 host 설정 편집과 MCP 재시작 없이
명시적으로 승인하고 원래 호출을 재개한다. HTTP의 OAuth/host 권한은 약화하지 않는다.

## 수직 절단

| 단계 | 결과 | 상태 |
|---:|---|---|
| 1 | primary boundary와 ephemeral external read grant 분리 | 완료 |
| 2 | one-time approval state와 grant/status/revoke service | 완료 |
| 3 | MCP elicitation과 read/write/import/Runner 호출 재개 | 완료 |
| 4 | cache runtime gate와 기존 시작 플래그 사전 승인 호환 | 완료 |
| 5 | portable memory runtime export와 경로 재바인딩 restore | 완료 |
| 6 | 공개 schema, 문서, 전체 verification, 설치본 교체 | 완료 |

## 완료 게이트

- focused package tests와 full repository verification 통과
- schema와 footprint 재생성 및 drift 없음
- 13-tool complete contract 34 KiB와 Runner 5,500-byte 예산 유지
- README, Unreleased, 문서 index, frozen 1.0 roadmap의 상태 설명 일치
- 공개 tree/history privacy gate 통과
- commit/push 후 새 MCP 실행 파일 설치
