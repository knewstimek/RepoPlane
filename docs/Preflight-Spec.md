# RepoPlane Environment Preflight Specification

상태: Accepted 1.0

## 1. 목적과 표면

Environment Preflight는 등록 capability를 실행할 현재 환경이 선언된 전제조건을
충족하는지 관찰한다. 별도 MCP tool을 추가하지 않는다. `run_prepare`가 실행에 필요한
preflight를 수행해 environment record와 plan에 연결하고, 과거 결과는
`project_records(kind=environment)`로 조회한다. 개발자용 `repoplane-dev preflight`는
기존 로컬 저장소 점검 명령으로 유지한다.

## 2. 검사 계약

항상 실제 실행 파일 identity와 현재 플랫폼을 검사한다. Manifest는 추가 검사를
선언할 수 있다.

| kind | 관찰 | 공개 결과 |
|---|---|---|
| `executable` | PATH 또는 workspace-relative 실행 파일, 선택적 version argv | basename, version 요약, identity hash |
| `file` | workspace-relative file 존재와 content hash | 상대 경로와 hash |
| `environment` | 환경변수 존재 | 이름과 present 여부; 값은 금지 |
| `git` | repository, HEAD, dirty 상태 | commit과 dirty; remote/identity는 금지 |
| `platform` | OS와 architecture | 정규화된 GOOS/GOARCH |

검사 requirement는 `required`, `recommended`, `informational`이다. `required`의
`failed`/`missing`/`unknown`만 plan을 실행 불가로 만든다. 나머지는 경고로 보존하며
`run_execute`가 별도 override token을 요구하지 않는다. 알 수 없는 executable에
관습적인 `--version`을 추측하지 않고 manifest가 명시한 argv만 실행한다.

검사와 응답은 개수, 시간, 출력 byte가 제한된다. probe stdout/stderr는 bounded 한 줄
요약만 저장하고 host 절대 경로, 환경 값, credential, 원시 진단은 record에 저장하지
않는다.

## 3. 호환성과 완료 조건

- 기존 preflight 선언이 없는 manifest도 실제 executable/platform 검사로 준비 가능하다.
- 기존 manifest의 조회 계약은 변하지 않으며 새 필드는 선택 사항이다.
- Windows와 Linux에서 PATH, workspace-relative executable, Unicode/공백 경로를 시험한다.
- required와 recommended의 차이, timeout, missing, unknown, redaction을 회귀 시험한다.
- executable, manifest revision 또는 선언 입력이 바뀌면 기존 plan 실행을 거부한다.
- 선언 입력과 무관한 worktree 변경은 plan을 무효화하지 않는다.
