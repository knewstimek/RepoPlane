# Project Control Plane MCP — 우선순위 중심 설계

> 문서 역할: 장기 아키텍처와 설계 근거를 설명한다. 구현 범위와 작업 순서는
> [MVP 명세](MVP-Spec.md), [구현 계획](Implementation-Plan.md),
> [저장 구조](Storage-Design.md)를 따른다. 구현 언어 결정은
> [ADR-0001](adr/0001-use-go.md)에 기록한다.

## 1. 목적과 범위

프로젝트에서 이미 만든 도구를 다시 발견하고, 필요한 원문만 읽으며, 실행과 검증 기록을 다음 작업에서 재사용하는 범용 MCP 서버를 설계한다.

**범용성은 공통 인터페이스와 데이터 계약을 뜻한다. 모든 언어·빌드 시스템·프로토콜을 자동 이해한다는 뜻이 아니다.**

이 문서는 기존 제안을 효용 대비 구현·유지 비용 기준으로 재검토한 설계다. 제시한 우선순위와 난이도는 제한된 기능 범위를 전제로 한 설계 판단이며, 실측 성능이나 개발 기간의 보장이 아니다. 특정 프로젝트와 언어에 종속된 예제는 사용하지 않는다.

가장 중요한 목표는 네 가지다.

1. 기존 도구를 몰라 같은 일을 다시 만드는 낭비를 줄인다.
2. 잘못된 경로·구성·오래된 결과를 사용한 오류를 줄인다.
3. 대형 출력의 선택적 조회로 모델에 전달하는 양을 줄인다.
4. “작업했다”와 “현재 변경 상태에서 검증했다”를 구분한다.

기존 셸, Git, 문서, 빌드 시스템과 테스트를 대체하지 않는다. MCP가 없거나 adapter가 실패해도 허용된 원문 조회와 기존 도구 사용이 가능해야 한다.

## 2. 채택 기능과 우선순위

P1은 공통 기반, P2는 반복 비용이 확인되는 작업에 적용할 기능이다. 시간순 개발 계획이 아니라 채택 우선순위다.

| 우선순위 | 채택 기능 | 해결하는 문제 | 제한된 범위에서의 난이도 |
|---|---|---|---|
| P1 | 도구 카탈로그·원본 검색·등록 누락 감사 | 있는 도구를 못 찾거나 재구현 | 낮음~중간 |
| P1 | Path Facts: 경로·규칙 위치·인코딩 확인 | 복사본 오수정, 규칙 누락, 문자 손상 | 중간 |
| P1 | 원문 범위 조회·JSON/JSONL/로그 projection | 같은 대형 파일을 계속 읽음 | 중간 |
| P1 | 명시적 검증 체크리스트와 결과 연결 | 테스트 누락, 예전 통과 결과 오용 | 낮음~중간 |
| P1 | 작은 작업 체크포인트 | 세션 변경 후 목표·미완료 검사 상실 | 낮음~중간 |
| P2 | 실행 영수증·선언된 artifact provenance | 명령과 출력의 생성 경위 재조사 | 중간 |
| P2 | 제한된 환경 사전 점검 | 잘못된 SDK·실행 파일·구성 사용 | 낮음~중간 |
| P2 | 등록 명령의 bounded runner | 긴 작업·출력 폭주·실행 추적 | 중간~높음 |
| P2 | 검증된 순수 변환에 한정한 캐시 | 같은 입력의 비싼 변환 반복 | 중간~높음 |
| P2 | 출처 있는 실패 메모 | 이전의 잘못된 접근 반복 | 낮음~중간 |

새로 제안한 기능 중에는 **Verification Oracle를 명시적 체크리스트로, Change Transaction을 체크포인트로, Observation Ledger를 실행 영수증으로 축소해 채택**한다. 이 정도면 새 추론 엔진 없이 기존 결과를 연결할 수 있다.

다음 기능은 공통 핵심에서 제외한다. 삭제된 의무를 다른 이름으로 되살리지 않는다.

| 제외 또는 선택 adapter로 한정 | 이유 | 필요한 경우의 대안 |
|---|---|---|
| 모든 프로세스·파일 읽기·네트워크의 전면 추적 | 운영체제별 구현, 권한, 성능, 관찰 사각지대 | 등록 작업의 영수증과 명시적 관찰 범위 |
| 범용 symbol/build/runtime 통합 그래프 | 언어·configuration별 해석 비용이 큼 | 기존 도구가 내보낸 특정 구성의 metadata 읽기 |
| 범용 ABI·wire compatibility 자동 판정 | 메모리 layout과 직렬화 계약은 다를 수 있음 | 기존 호환성 검사기를 검증 목록에 등록 |
| 자동 Context Pack·영향 테스트 자동 축소 | 검색 누락을 충분성 판단으로 오인하기 쉬움 | 독립 검색과 명시된 필수 검사 유지 |
| 범용 behavioral differential 엔진 | 시나리오·환경·비결정성 정규화가 필요 | 기존 golden test·snapshot test 재사용 |
| 자동 claim/negative knowledge 확정 | 실패와 원인, 기록과 사실을 혼동 | 출처와 적용 조건이 있는 메모 |
| 전역 copy lineage 추론 | 동일 내용·비슷한 이름으로 계보를 증명할 수 없음 | 같은 basename 후보와 요청된 파일의 hash 비교 |
| 범용 encoding-safe patch 엔진 | 동시 편집·메타데이터·인코딩 보존까지 요구 | 기존 편집 도구와 변경 전후 byte 검증 |
| 자동 위험도·Uncertainty Budget | 추정의 신뢰성과 효과 검증 비용 | 확인하지 못한 항목을 명시적으로 반환 |

## 3. 기존 설명 재점검과 정정

| 이전에 과장되거나 혼동된 설명 | 이 설계의 정정 |
|---|---|
| MCP이면 Markdown보다 토큰이 싸다 | Markdown도 검색·부분 읽기로 같은 효과를 낼 수 있다. 검색 방식과 실제 반환량을 비교한다. |
| tool 수를 줄이면 효율적이다 | 설명·입력 schema·선택된 capability schema·후속 호출까지 모두 비용이다. |
| Resource로 바꾸면 컨텍스트 비용이 사라진다 | Resource를 언제 문맥에 넣는지는 클라이언트가 결정한다. 자동 절감은 보장되지 않는다. |
| 결정론적 projection은 품질 손실이 없다 | 동일 질의의 재현성이 있을 뿐이다. 잘못 선택한 필드와 필터가 필요한 정보를 빠뜨릴 수 있다. |
| truncated를 공개하면 탐색 품질이 유지된다 | 숨긴 누락을 줄일 뿐이다. 사용자가 남은 결과를 확인하지 않으면 여전히 놓칠 수 있다. |
| 전체 match 수를 항상 반환해야 한다 | 전수 검색이 끝나지 않았으면 정확한 수를 모른다. lower bound 또는 unknown으로 표시한다. |
| 실행 전후 snapshot으로 실제 읽은 파일을 알 수 있다 | 종료 시 남은 변경만 비교한다. 읽기, 잠깐 생성 후 삭제, 원상복구, 네트워크 사용은 알 수 없다. |
| 한 번 생성된 파일이면 영구 artifact 역할이 확정된다 | 특정 실행의 출력이라는 기록만 얻는다. 같은 파일이 다른 작업의 입력일 수 있다. |
| 입력 hash가 같으면 재실행할 필요가 없다 | 숨은 의존성·시간·외부 상태·환경이 있으면 성립하지 않는다. 캐시 적격성부터 확인한다. |
| encoding round-trip 성공이면 encoding이 맞다 | 선택한 codec에서 byte가 보존된다는 검사다. 원래 의도한 문자인지는 별도 문제다. |
| Git copy/rename 결과는 확정된 계보다 | 유사도 기반 탐지일 수 있다. 탐지 옵션과 근거를 남긴다. |
| 자유 형식 작업 규칙을 MCP가 완전히 해석한다 | 파일 위치·선언된 scope를 찾을 수 있다. 자연어 의미와 호스트별 우선순위를 독자적으로 확정하지 않는다. |
| 테스트 통과나 사용자 승인으로 일반 주장이 참이 된다 | 명시된 검사 범위의 통과나 승인 사실만 기록한다. 전역 정확성의 증명이 아니다. |
| 제한된 그래프의 closure로 충분한 테스트를 증명한다 | 알려진 관계의 계산일 뿐이다. 모든 동작과 결함을 다룬다는 보장이 없다. |

기존 문서의 절 번호 불일치, 응답에서 `items`가 배열/객체로 바뀌던 문제, 서로 다른 검색 dialect의 모호한 혼용도 정리한다.

## 4. LLM·서버·프로젝트 관리자의 책임

MCP는 통신 규약이다. 여기서 “서버가 판정한다”는 말은 **서버에 구현한 parser·검색기·검사 코드가 판정한다**는 뜻이며, MCP 자체가 이해하거나 학습한다는 뜻이 아니다.

| 주체 | 담당 | 담당하지 않는 것 |
|---|---|---|
| 프로젝트 관리자·신뢰된 설정 | 도구 설명, 파일 역할, 필수 검증, 허용 실행 범위 선언 | 모든 원문과 실행 결과의 수작업 복제 |
| 서버 | 검색, hash, 범위 읽기, schema 검사, 영수증 저장, 기록 간 연결 | 증거 없는 역할 확정, 결과의 의미적 충분성 판단 |
| LLM | 질의 선택, 후보 확인, 구현 판단, 작업 설명·메모 작성 | hash 계산 결과 조작, 미지원 영역을 확인 완료로 취급 |
| 호스트·운영체제 | 인증, 승인, 격리, 권한 및 실행 제한 | 카탈로그 설명만 믿고 안전성을 보장 |

### 4.1 결정론성의 의미

고정된 원본 snapshot `S`, 질의·설정 `Q`, 엔진 버전 `V`에 대해 같은 결과 `F(S,Q,V)`가 나오도록 설계한다. 정렬, 인코딩, locale, 숫자 처리와 pagination 기준도 고정한다.

하지만 결정론적 계산이라도 잘못된 입력과 불완전한 모델에 대해 매번 같은 오답을 낼 수 있다. 실제 명령 실행, 가변 filesystem과 네트워크까지 자동으로 결정론적이 되는 것은 아니다.

따라서 다음을 별도로 보고한다.

- `basis`: declared / observed / derived / heuristic / llm_proposed.
- `validity`: current / stale / unknown. **명시된 조건에 대한** 유효성이다.
- `coverage`: 실제로 열거·검색·해석한 범위와 실패.
- `evidence`: 원본 ref, 위치, revision, 관찰 방법.

`basis=declared`는 “문서에 그렇게 적혀 있다”는 뜻이지 내용이 반드시 참이라는 뜻이 아니다. evidence가 없으면 없다고 반환하며, 임의의 confidence 1.0을 붙이지 않는다.

### 4.2 Artifact와 파일 역할

파일의 역할은 하나로 고정하지 않는다. 생성된 소스는 `generated`이면서 빌드 입력인 `source`일 수 있고, 실행 결과는 다음 실행의 `input`일 수 있다.

`reports/checks.json`에 대한 예:

- manifest에서 output으로 선언됨: `basis=declared`.
- 격리된 실행 디렉터리에서 해당 run의 출력으로 확보됨: `basis=observed`.
- 공용 디렉터리의 전후 비교에서 변경됨: 변경 관찰이며 생성 주체는 `unknown`일 수 있음.
- 파일명이 result 또는 output임: `basis=heuristic`.
- 아무 근거 없음: 역할 미분류. 읽기는 가능하되 자동 삭제·덮어쓰기 권한으로 쓰지 않음.

## 5. 작은 MCP 표면과 schema 비용

조회 중심 기본 tool은 다섯 개, 실행 adapter를 켰을 때 추가되는 tool은 세 개로 제안한다. 개수 자체보다 호출 성공률과 직렬화된 전체 크기를 측정한다.

| Tool | 좁고 안정적인 책임 |
|---|---|
| `catalog_query` | 검색·항목 조회·전체 순회·미등록 후보·카탈로그 상태 |
| `path_explain` | 경로 사실, 적용 규칙 위치, encoding, basename 중복 |
| `workspace_search` | filename/exact/regex 검색, 선택적 기존 Git 이력 검색 |
| `data_query` | 원문 범위·구조화 데이터의 필터와 projection |
| `project_records` | artifact·실행·검증·체크포인트·환경·메모 기록 검색/조회 |
| `run_prepare` | 등록 capability의 schema 검증과 실행 계획 생성 |
| `run_execute` | 승인·상태 재검증 후 계획 실행 |
| `run_inspect` | 상태·출력 ref·중지 요청·완료 결과 |

기본 tool의 입력은 자주 쓰는 인자를 명시적으로 제공한다. 전부 `operation:string, arguments:object`인 만능 tool로 만들지 않는다.

상세 실행 capability는 `catalog_query(mode=get, id=...)`로 필요한 것만 조회한다. 짧은 argument schema는 항목과 함께 반환하고, 긴 schema는 `mode=schema`로 조회한다. 실행 요청에는 선택한 `capability_revision`을 넣고 서버가 해당 revision의 schema로 검증한다.

- 새로운 schema를 발견할 때마다 설치나 재시작을 요구하지 않는다.
- 같은 revision을 이미 조회했다면 반복 조회하지 않는다.
- 알 수 없는 인자, 범위를 벗어난 경로, 미지원 옵션을 조용히 무시하지 않는다.
- validation 오류에는 오류 필드와 올바른 schema ref만 간결하게 제공한다.
- 미지원 adapter는 검색 결과와 실행 가능 목록에서 구분한다.
- 명세 조회 실패를 다른 임의 실행 경로로 우회하지 않는다.
- Resource는 선택 경로이며, Resource 사용을 지원하지 않는 클라이언트도 tool 조회만으로 같은 정보를 얻는다.
- 호환성을 위한 structured/text 중복 표현이 클라이언트에서 이중 주입되는지 실제 확인한다. 규약상 필요한 응답을 임의로 제거해 절감하지 않는다.

### 5.1 LLM 사용 계약

에이전트에게 이 전체 설계서를 매번 읽히지 않는다. 짧은 진입 규칙만 제공한다.

1. 도구를 새로 만들거나 반복 작업을 수행하기 전에 관련 카탈로그를 검색한다.
2. 찾은 항목의 실행법과 현재 사용 조건을 확인한다.
3. 편집 대상이 모호하거나 인코딩 위험이 있을 때 경로 정보를 확인한다.
4. 대형 결과는 필드·범위로 조회하고, `partial`이나 미확인 영역을 숨기지 않는다.
5. 관련 manifest와 검증 기록을 바뀐 파일과 함께 갱신한다.
6. schema 오류는 제공된 계약을 조회해 수정한다. 이름이나 인자를 추측해 무한 재시도하지 않는다.

모든 한 줄 수정에 카탈로그·환경·그래프 전체 조회를 강제하지 않는다. 서버가 모든 호출마다 새 계획과 설명을 길게 반환하는 것도 피한다.

## 6. 카탈로그와 등록 누락 관리

### 6.1 원본과 색인

도구 설명 원본은 저장소 안의 YAML/JSON 또는 frontmatter가 있는 Markdown에 둔다. 이미 문서가 있다면 경로와 설명을 재사용한다. 검색용 SQLite 등은 재생성 가능한 cache다.

다음은 선언 파일 예시다. `arguments`는 JSON Schema 자체가 아니라 서버가 JSON Schema로 변환·검증하는 간략 명세다. 경로와 ID는 설명용이다.

```yaml
id: schema.validate
revision: 1
summary: 설정 파일이 지정 schema와 일치하는지 검사
use_when:
  - 설정 필드 추가 또는 수정
  - 설정 형식 오류 조사
tags: [config, configuration, 설정, schema, validation, 검증]
execution:
  kind: cli
  executable_ref: tools.validator
  cwd: repository
  argv_template: ["--schema", "{schema}", "--input", "{input}", "--report", "{report}"]
  trusted_for_run: false
arguments:
  schema:
    type: project_path
    required: true
  input:
    type: project_path
    required: true
  report:
    type: project_path
    default: reports/schema-check.json
inputs: ["{schema}", "{input}"]
outputs: ["{report}"]
checks: [config.schema]
docs: [docs/configuration.md]
cache_policy: disabled
```

`trusted_for_run`은 문서 작성자가 자기 자신에게 실행 권한을 부여하는 필드가 아니다. 호스트의 신뢰 정책에서 부여한 상태를 반영하며, 저장소 파일을 수정하는 것만으로 승인되지 않는다.

처음에는 항목 0개여도 정상 작동한다. 도구가 생길 때 항목을 추가하면 된다. 모든 script가 복잡한 입출력 schema나 실행 adapter를 가져야 등록되는 구조는 피한다. 이름·목적·사용 조건·원본 위치만 있는 문서 항목도 허용한다.

### 6.2 검색

기본은 목적·태그·이름의 lexical 검색이다. 한국어와 영어 표현 차이는 태그·별칭·조회어 확장으로 보완한다. tokenizer와 언어에 따라 결과가 달라지므로 FTS만 붙이면 자연어 검색이 해결된다고 가정하지 않는다.

- 기본 반환: ID, 목적 한 줄, 매치 이유, 실행 가능 여부, 상세 ref.
- semantic 검색은 선택 사항이며 별도 후보로 표시한다.
- 순위가 낮은 후보를 숨기지 않고 목록 순회 경로를 제공한다.
- 단순 점수로 정확 매치를 지우지 않는다. 필요하면 통합 순위를 보조 제공하되 각 채널과 원본 결과를 유지한다.

### 6.3 등록 누락 감사

선언된 `tools/`, `scripts/`, 프로젝트 manifest 경로에서 실행 후보와 등록 항목을 대조한다. 확장자나 shebang으로 찾은 것은 후보일 뿐, 재사용 가능한 도구임을 증명하지 않는다.

보고 대상:

- 미등록 후보.
- 존재하지 않는 원본 경로, ID 충돌, schema 오류.
- 이전 확인 이후 executable·script·manifest가 바뀐 항목.
- 아직 지원하지 않는 manifest와 해석 실패.
- 검사 scope, 제외 정책, 마지막 검사 기준.

등록은 에이전트나 사람이 원본 manifest를 수정하고 서버가 재색인하는 방식으로 통일한다. 안전한 parser를 쓰고 사용자 정의 YAML tag 등을 실행하지 않는다. 소스만 변경됐다고 명령 사용법도 자동으로 바뀌는 것은 아니므로 `needs_review`를 구분한다.

## 7. Path Facts, 검색과 원문 조회

### 7.1 경로 설명

`path_explain(path, action)`은 경로를 받아 다음을 **확인 가능한 항목만** 반환한다.

- repository/worktree/nested repository identity와 상대 경로.
- tracked/untracked/ignored 상태와 realpath.
- symlink/junction/hardlink 여부. 지원하지 않는 OS 항목은 unknown.
- 선언된 파일 역할과 근거; basename이 같은 후보.
- 규칙 파일 위치·원문 ref·scope. 현재 클라이언트의 rule profile을 사용.
- BOM, newline 분포, 마지막 newline, 명시된 encoding과 관찰 결과.
- 선언된 configuration 또는 수정·검증 의무와 출처.

`tracked`, 동일 hash, `src/` 경로만으로 canonical source를 확정하지 않는다. 선언 충돌은 양쪽을 보여준다. 명확한 근거 없는 `not_roles`도 생성하지 않는다.

### 7.2 규칙과 encoding의 한계

작업 규칙을 찾는 것과 자연어 규칙을 해석하는 것은 다르다. 서버는 프로젝트별·클라이언트별 명시 profile로 위치를 찾고 원문을 제공한다. 호스트가 로드한 더 높은 우선순위 지침을 대체하지 않는다. 명령형 규칙 원문을 검색 요약 몇 줄로 대체하지 않으며, 생략이 있으면 적용 규칙 확인 완료로 표시하지 않는다.

Encoding은 명시 선언, BOM, 선택 codec에서의 decoding 성공, 휴리스틱 감지를 구분한다. CP949와 EUC-KR을 서로 바꿔 써도 된다고 가정하지 않는다. Round-trip 성공은 **해당 codec으로 byte가 보존됨**만 뜻한다.

편집은 기존 도구에 맡긴다. 필요하면 원본 hash와 변경 전후의 비편집 byte·newline 보존을 검사하는 작은 validator를 등록한다. 변환·손실·동시 수정이 발견되면 그대로 덮어쓰지 않는다.

### 7.3 Workspace search

기본 backend는 파일 열거, ripgrep 계열 검색과 Git 이력이다. 기존 symbol index가 있을 때만 선택 adapter로 연결한다. filename/exact/regex/docs/semantic의 근거를 구분한다.

검색 시 반드시 선언할 조건:

- root, include/exclude, hidden/ignored/generated/vendor 처리.
- text encoding, case sensitivity, regex engine/version.
- Git 이력의 revision·ref·기간 범위와 shallow 여부.
- 파일/line/record 중 무엇을 match 한 건으로 세는지.
- 정렬 기준, 시간·입력 크기 제한과 실패.

엔진의 기본 제외 정책을 모르는 채 `files_skipped=0`이라고 반환하지 않는다. 접근할 수 없는 디렉터리 내부 파일 수는 unknown이다. generated/vendor도 무조건 전수 조회하지는 않되, 포함 여부와 명시적 확장 경로를 제공한다.

검색기가 내부적으로 큰 파일을 읽는 비용은 모델 입력 토큰과 별개지만 CPU·I/O·지연 비용은 존재한다.

### 7.4 Data query

텍스트 line/byte range, 로그 exact/regex, JSON·JSONL·CSV/TSV 필터와 필드 선택을 핵심으로 둔다. XML·test report는 사용 중인 형식에 대한 adapter가 있을 때만 제공한다.

질의 dialect를 명시하고 엔진 버전을 고정한다. JSONPath/JMESPath와 자체 filter를 하나의 문법처럼 혼용하지 않는다. 예를 들어 JMESPath selection 후 pagination을 수행하는 요청:

```json
{
  "ref": "artifact:report-example",
  "dialect": "jmespath",
  "expression": "items[?status == 'failed'].{id: id, reason: reason}",
  "item_limit": 20,
  "byte_limit": 8192
}
```

- 필터·projection·정렬·pagination의 순서를 계약으로 고정한다.
- 대형 JSON을 전부 메모리에 올리지 못하면 명시적으로 거부하거나 지원하는 streaming/index 경로를 사용한다.
- group/count/sort는 전수 입력이 필요할 수 있다. 부분 집계를 전체 결과처럼 표시하지 않는다.
- 누락 필드, null, 잘못된 레코드, 숫자 정밀도, 문자열 ID 처리 정책을 공개한다.
- XML 외부 entity, 임의 함수/eval, schema의 자동 외부 URL 조회는 금지한다.
- 임의 SQL·shell·사용자 코드를 질의식으로 실행하지 않는다.
- raw ref와 원문 range를 제공하되 접근권한·비밀정보 보호를 우회하지 않는다.
- 하나의 매우 큰 line/item도 별도 잘림 상태나 원본 ref로 처리한다.
- 로그의 같은 오류 그룹은 원문 위치를 따로 조회하게 한다. 모든 위치 배열을 기본 응답에 복제하지 않는다.

Projection은 LLM 재요약에 따른 변형을 피한다. 그러나 조회한 필드만으로 판단이 충분한지 확인하는 책임은 남는다.

## 8. 응답·pagination·freshness 공통 계약

기본 응답은 작게 유지한다. `items`는 항상 배열이며 상세 metadata는 ref로 확장한다. 필수 경고와 불완전 상태는 예산을 이유로 제거하지 않는다.

다음 예는 scope 내 검색을 끝내 전체 12건을 찾았지만 한 건만 반환한 경우다.

```json
{
  "status": "ok",
  "items": [
    {
      "id": "schema.validate",
      "summary": "설정 schema 검사",
      "basis": "declared",
      "evidence": [{"ref": "source:manifest-example", "line_start": 1, "line_end": 8}]
    }
  ],
  "counts": {"matched": 12, "relation": "exact", "returned": 1},
  "scan": {"state": "complete", "scope_ref": "scope:catalog-example"},
  "truncated": true,
  "next_cursor": "cursor:example-page-2",
  "snapshot_ref": "snapshot:catalog-example",
  "warnings": []
}
```

### 8.1 서로 다른 상태를 혼동하지 않기

- `status`: ok / partial / error / unsupported. 실행·해석의 상태다.
- `scan.state`: complete / partial / not_applicable. 선언된 scope를 처리했는지다.
- `counts.relation`: exact / lower_bound / unknown / not_applicable.
- `truncated`: 알려진 결과 중 인라인에 싣지 못한 것이 있는지다. 아직 모르겠으면 null.
- `validity`: current / stale / unknown. 기록된 조건과 현재 상태의 일치 여부다.

시간 제한으로 50건만 발견하고 검색을 중단했으면 `matched=50, relation=lower_bound`로 보고한다. 상위 K개의 semantic 후보는 저장소의 모든 관련 결과 수가 아니다. 알 수 없는 수를 0으로 채우지 않는다.

### 8.2 Cursor와 원본 identity

Cursor는 query hash, 정렬, snapshot/index generation, 다음 위치와 만료 시각에 연결한다. 예전 cursor로 변경된 index를 이어 읽어 결과가 섞이면 안 된다.

수정 가능한 작업 디렉터리는 다중 파일의 원자적 snapshot이 아니다. 낮은 구현 비용을 원하면 고정된 검색 결과·레코드 집합을 페이지화하고, 후속 원문 조회 시 파일 hash를 재확인한다. 원본이 바뀌면 `source_changed`로 알리고 재조회하게 한다. 진짜 snapshot이 아니면 snapshot 일관성을 주장하지 않는다.

Hash 식별자는 내용 확인을 돕지만, 해당 byte를 실제로 보관했다는 보장은 아니다. ref에는 `immutable_blob`, `git_revision`, `mutable_path` 중 저장 성격을 기록한다. Blob이 삭제되었거나 만료됐다면 명시적 오류를 반환하고 다른 현재 파일로 바꿔치기하지 않는다.

### 8.3 예산

- `item_limit`, `byte_limit`, `time_limit`을 실제로 적용한다.
- `token_budget`은 지원 tokenizer가 고정된 경우만 측정치를 제공한다. 그 외에는 추정임을 표시한다.
- 큰 결과의 수와 목록을 얻기 위해 매번 전수 재검색하지 않도록 고정 결과를 재사용한다.
- `detail=full`도 무제한 반환이 아니다. 큰 결과는 ref와 별도 조회로 제공한다.
- 거대한 evidence·location·dependency 배열도 페이지화한다.
- 같은 원문을 응답의 summary, items, evidence에 반복 복제하지 않는다.
- 정책상 원문 저장이 가능한 결과는 보존하고, 보존 불가·로그 한도·redaction·TTL은 별도로 알린다.

## 9. 실행 영수증, Artifact 기록, 환경 점검

### 9.1 실행 영수증: 관찰을 저렴하게 제한

등록된 작업 또는 신뢰된 wrapper로 실행한 명령에 한해 다음을 기록한다.

- run ID, capability/revision, workspace ID, configuration.
- 실제 executable identity, argv, cwd, 시작·종료, exit code와 termination reason.
- **선언된** input과 관련 config의 hash.
- 전후 비교한 output 경로, 확보한 artifact ref와 해시.
- stdout/stderr ref, capture 한도, 누락 여부.
- 사용한 환경 점검 ref와 검사 방법.
- 동시 실행·쓰기 관찰 한계.

실행 실패 시에도 영수증을 남긴다. 비정상 종료에서는 `interrupted`와 확보한 부분 기록을 보존한다. 수동 실행은 사용자가 원할 때 기존 보고서·명령 기록을 import하고 `imported` 출처를 붙인다.

**모든 셸 호출을 자동 관찰한다고 가정하지 않는다.** Runner 밖에서 일어난 실행은 missing coverage다. 한 번의 관찰을 모든 실행의 dependency 계약으로 승격하지 않는다.

| 관찰 수단 | 확인 가능한 것 | 확인할 수 없는 것 |
|---|---|---|
| argv와 input 선언 | 무엇을 실행했고 어떤 입력을 지정했는지 | 프로세스가 실제 읽은 모든 파일 |
| 시작·종료의 경로/hash 비교 | 그 두 시점 사이 남은 변경 | 일시 파일, 읽기, 원복, 네트워크·DB 변화 |
| 격리된 run 디렉터리의 출력 수집 | 해당 run에서 확보한 출력 | 디렉터리 밖 부작용 전부 |
| 기존 test report 파싱 | 보고서가 기록한 테스트와 상태 | 기록되지 않은 테스트·숨겨진 오류 |

공용 worktree에서 다른 에이전트나 사용자가 변경할 수 있다면 `writer_attribution=unknown`으로 둔다. 정확한 귀속이 필요하면 별도 출력 디렉터리나 worktree를 사용하되, 그것만으로 OS 격리가 성립하는 것은 아니다.

### 9.2 Artifact provenance는 작은 관계 목록으로

전용 그래프 DB를 요구하지 않는다. run/input/output/reference 테이블과 경로 역색인으로 시작 가능한 데이터 모델이다. `project_records(kind=artifact, ...)`로 다음을 조회한다.

- 어느 run에서 등록된 출력인지, 선언인지 관찰인지.
- 어떤 입력·설정·도구 revision을 기준으로 생성됐는지.
- 어떤 등록된 작업이 이 경로를 입력으로 선언했는지.
- 현재 출력의 무결성과 선언 조건의 변화.

상태를 분리한다.

```json
{
  "integrity": "matches_recorded_hash",
  "validity": "current",
  "validity_scope": "declared_dependencies",
  "dependency_coverage": "declared_only",
  "reusable": "unknown",
  "reason": "선언 외 의존성 및 외부 상태는 검증되지 않음"
}
```

입력이 바뀌면 stale이며, 의존성이 같아도 재사용 적격성을 확인하지 못했으면 `reusable=true`가 아니다. 소비자 목록도 알려진 선언의 목록이지 전체 소비자 증명이 아니다.

### 9.3 Environment Preflight

환경 전체를 매번 덤프하지 않는다. capability가 명시한 다음 항목만 검사한다.

- 실행 파일의 실제 해석 경로와 hash 또는 version.
- 지정 compiler/runtime/SDK와 configuration의 존재.
- 필요한 lockfile/config의 hash.
- 필요한 locale·code page·architecture.
- 필요한 환경변수·credential의 존재 여부. 값은 반환하지 않음.

버전 확인 command도 코드를 실행한다. 신뢰된 도구와 제한된 probe만 사용하며 알 수 없는 프로그램에 `--help`나 `--version`을 자동 실행하지 않는다.

Lockfile hash 일치가 실제 설치 상태 일치를 증명하지는 않는다. 미지원 SDK 감지와 외부 서비스 상태는 unknown으로 남긴다. 원격 endpoint 확인은 네트워크 허용과 별도 비용을 가진 작업이다.

## 10. 검증 체크리스트와 작업 체크포인트

### 10.1 Verification Checklist

“어떻게 맞다고 증명하지?”를 범용 추론 문제로 풀지 않고, 기존 테스트와 규칙을 명시적으로 연결한다.

```yaml
id: config.schema
revision: 1
applies_to:
  paths: ["config/**", "schemas/**"]
required: true
capability: schema.validate
configurations: [default]
success:
  exit_code: 0
  report_required: true
  report_schema: check-report.v1
  minimum_executed_checks: 1
```

검증의 입력 범위와 configuration을 기록하고 run 결과를 연결한다. Runner가 없어도 기존 CLI·CI report를 import할 수 있다. 프로젝트의 report parser를 재사용하고 별도의 “정답 판단 AI”를 필수로 넣지 않는다.

상태: passed / failed / not_run / blocked / unknown / stale. Skipped는 passed가 아니다. 성공 exit code만 있고 필수 report가 없거나 테스트 0건이면 검증 완료가 아니다.

다음 조건을 함께 확인한다.

- 어떤 revision·working-tree fingerprint를 검사했는가.
- 어떤 configuration과 검사 목록이 적용됐는가.
- 실제 실행된 검사와 제외·skipped 수는 얼마인가.
- 실행 이후 관련 변경이 발생했는가.

관련 경로를 완전히 알지 못하면 보수적으로 선언된 전체 scope 변경에 결과를 stale 처리한다. 실패의 원인이나 모든 동작의 올바름은 테스트 통과에서 자동 도출하지 않는다.

### 10.2 Task Checkpoint

데이터베이스 transaction 같은 원자적 변경·자동 rollback을 약속하지 않는다. **작업 상태 기록**으로 정의한다.

| 자동으로 기록 가능한 항목 | 에이전트·사용자가 입력해야 할 항목 |
|---|---|
| 기준 commit, dirty 상태, 선택 경로 hash | 목표·의도·결정 이유 |
| 등록된 run ID와 report ref | 다음에 하려던 작업 |
| 알려진 검증의 통과·실패·미실행 | 미확인 위험에 대한 설명 |
| manifest와 산출물 변경 사실 | 실패 원인과 대안의 해석 |

Git baseline은 commit만으로 부족하다. 기존 staged/unstaged/untracked 변경도 범위 내에서 기록하여 사용자 작업과 이번 변경을 혼동하지 않게 한다. VCS가 없으면 제한된 경로 manifest를 기준으로 사용한다.

작업 시작·명시적 체크포인트·등록된 run 종료 시 실제 사건을 durable record로 저장한다. 서버가 관찰한 사건은 에이전트의 메모 저장 호출에 의존하지 않는다. 단, 서버 외 작업과 의도는 자동 복구되지 않는다.

작업 종료 시 상태는 `checks_satisfied`, `incomplete`, `blocked` 등으로 표시한다. “정확함이 증명됨”이라는 이름은 쓰지 않는다. 다음 세션은 목표 한 줄, 남은 검사, 마지막 run ref를 먼저 읽고 필요한 것만 확장한다.

다중 에이전트의 claim·소유권은 잠금이 아니다. 기록 충돌에는 revision을 검사하며, 코드 충돌은 기존 Git/worktree·편집 도구로 해결한다. 서버가 저장소 전체를 자동 커밋·stash·reset하지 않는다.

### 10.3 실패와 결정 메모

기존 Markdown 메모를 다음 필드로 색인한다: 종류, 적용 scope/configuration, 내용, evidence, 작성자, 검증 방법, invalidation 조건. 종류는 decision / failed_attempt / resolved_failure / limitation 정도로 작게 유지한다.

“실패했다”는 기록과 “이 원인으로 실패했다”는 해석은 별개다. 에이전트가 작성한 원인은 `llm_proposed`이며, 반복 실패 횟수만으로 verified가 되지 않는다. 충돌 메모는 둘 다 반환하고 오래된 제한은 superseded 처리한다.

메모 저장 누락을 없앴다고 주장하지 않는다. 자동 영수증·코드·테스트 결과를 복구 경로로 남기고 중요한 의미 정보만 작업 종료 때 점검한다. 별도 대화 원문 전량 수집은 필수 기능이 아니다.

## 11. 선택 실행 계층과 보수적인 캐시

검색만 필요한 프로젝트는 실행 계층을 사용하지 않아도 된다. 실행 계층의 난이도를 “argv 실행이면 끝”으로 평가하지 않는다.

### 11.1 Prepare → Execute → Inspect

- `run_prepare`: capability revision, 검증된 arguments, cwd, 실제 argv, 선언 입력·출력, 전제조건, 허용 경로, 예상 fan-out과 bounds를 계획으로 고정한다.
- `run_execute`: plan ID를 받되, ID 보유가 승인 자체는 아니다. 실행 주체의 권한과 정책을 별도로 검사한다.
- `run_inspect`: 상태와 로그·report ref를 반환한다. 중지는 실행 주체의 권한을 검사하는 명시적 action이다.

계획 수와 캐시 후보 수는 실제 열거 가능한 범위에서만 exact로 표시한다. 예상 시간은 과거 실행 통계 등 근거가 있을 때만 estimate로 제공한다. 런타임 동적 fan-out이 알려지지 않으면 unknown이다.

계획 생성 과정에서 프로젝트 script를 실행해야 한다면 그것은 단순 조회가 아니다. 별도 실행 승인과 제한이 필요하다. CMake configure, metadata refresh 등도 이름만으로 부작용이 없다고 가정하지 않는다.

### 11.2 실행 제한

- 승인된 capability와 검증된 인자로 실행한다. 임의 shell 문자열을 받지 않는다.
- 선택 실행 파일과 manifest revision이 바뀌면 재계획·재승인한다.
- timeout, cancel, 로그·디스크 한도와 제공 가능한 process-tree 제한을 적용한다.
- CPU·메모리·네트워크 격리는 OS/backend 지원 여부를 공개한다. 지원되지 않는 필수 제한은 조용히 생략하지 않고 실행을 거부한다.
- 실제 executable에는 인자 자체가 위험할 수 있다. argv 배열 사용만으로 안전성이 보장되지는 않는다.
- Windows quoting, child process 종료, crash recovery는 해당 OS의 검증된 API/라이브러리를 사용한다.
- 변경 명령의 자동 재시도는 기본 비활성화한다. idempotence를 명시·검증한 작업만 제한 재시도한다.
- 실행 직전 hash 재확인만으로 check와 사용 사이 race가 완전히 사라지지 않는다. 필요한 작업은 고정 입력 복사·지원되는 잠금·격리로 보호한다.
- stdout/stderr는 별도 stream identity와 offset으로 기록한다. 두 stream의 수신 시각만으로 프로세스 내부의 정확한 발생 순서를 주장하지 않는다.

### 11.3 Cache eligibility

기본값은 `cache_policy=disabled`다. 입력·도구·설정·환경이 통제되고 외부 부작용 없는 변환만 명시적으로 적격성 검토를 거쳐 켠다. 기존 빌드 시스템 cache가 있으면 그것을 우선 사용한다.

캐시 키 재료:

```text
capability revision + ordered argv + tool/runtime identity
+ declared input/config content hashes + configuration
+ controlled environment fingerprint + output contract version
```

정규화 중 argv 순서나 의미 있는 공백·대소문자를 지우지 않는다. 민감값을 키 로그에 쓰거나 낮은 엔트로피 비밀을 일반 hash로 공개하지 않는다. Credential은 보통 값 대신 호스트가 관리하는 version identity를 사용하고, 재사용 판단이 불가능하면 캐시를 끈다.

재사용 전에는 적격 정책, 모든 선언 의존성, 출력 blob 존재·hash, report 검증, 현재 목적지의 덮어쓰기 정책을 확인한다. 외부 상태·현재 시각·randomness·동적 파일 검색이 통제되지 않으면 키가 같아도 재사용하지 않는다.

`validity=current`와 `cache_hit`는 다르다. False hit가 관찰되지 않았다고 미래에 없다고 보장하지 않는다. 선택한 적격 작업에서 cache on/off 결과 비교와 의존성 변경 테스트를 수행한다.

## 12. 저장 구조와 변경 경계

필수 기반은 파일 원본, 가벼운 record store, 재생성 가능한 검색 색인이다. 전용 그래프 DB, embedding service, 상시 추론 모델은 필수가 아니다.

| 데이터 | 원본 | 갱신 주체 |
|---|---|---|
| 도구·검증·명시 역할 | 저장소의 선언 파일 | 사용자·에이전트의 일반 파일 수정 |
| 코드·문서·실패 메모 | 기존 파일·Git | 기존 개발 workflow |
| run·환경·확보된 출력·검증 결과 | durable record와 artifact 저장소 | 서버·신뢰된 wrapper/importer |
| 작업 목표·후속 의도 | 작은 작업 기록 | 사용자·에이전트의 명시적 기록 |
| 검색·경로 연결 | cache/index | 서버 재색인 |

기본 write 경로는 원본 manifest·작업 메모의 일반 파일 편집과 서버의 실행 후 기록이다. 기존 CI report는 명시적인 로컬 importer로 들여온다. `project_records`는 이 기록을 읽기만 하며 숨은 수정이나 실행을 하지 않는다. 선언 파일 검증 실패는 기존 정상 항목을 잘못된 새 정보로 덮어쓰지 않고 오류 상태로 공개한다.

동시 쓰기는 SQLite transaction 등 검증된 수단과 expected revision으로 처리한다. 다중 서버 프로세스가 JSONL에 임의 append하는 것을 안전한 동시 기록이라고 가정하지 않는다.

프로젝트 ID와 workspace ID를 분리한다. 같은 remote의 worktree도 dirty 상태·출력·검증 결과는 다를 수 있다. 공유할 수 있는 선언과 공유하면 안 되는 실행 상태를 구분한다. 원격 URL은 프로젝트 identity의 힌트이며 독자적으로 접근 권한을 부여하지 않는다.

## 13. 보안·정보 보존 정책

- 승인된 workspace root와 실제 경로에 대한 접근을 검사한다. symlink·junction·race를 단순 문자열 prefix로 처리하지 않는다.
- 카탈로그·문서·로그에 적힌 명령은 조회 데이터다. 그것을 상위 지침·승인·비밀 접근 권한으로 취급하지 않는다.
- 실행 권한, destructive 작업, 외부 서비스 변경, 네트워크 접근은 별도 정책으로 관리한다.
- 조회 API는 source와 외부 상태를 바꾸지 않는다. 내부 index/cache 생성은 공개한 저장 경로와 정책 안에서만 수행한다.
- 비밀·개인정보가 포함될 수 있는 로그는 접근권한과 보존기간을 정한다. redacted view와 접근 제한된 원본을 구분하고, 저장하지 못한 원문을 보존됐다고 하지 않는다.
- Blob과 run record의 TTL, 용량 제한, 삭제 시 ref 동작을 명시한다. GC는 보존 중인 run이 참조하는 데이터를 몰래 지우지 않는다.
- 인덱스·DB·일반 파일 생성은 프로젝트 권한 범위 안에서만 수행한다. 위험한 실행 adapter를 검색 기능의 필수 의존성으로 두지 않는다.

## 14. 실제 사용성·품질 검증

설계만으로 “LLM이 잘 쓴다”, “탐색 품질 저하가 없다”, “토큰을 절약한다”고 결론내리지 않는다. 같은 저장소 snapshot과 작업으로 비교한다.

### 14.1 비교 대상과 측정

비교 대상은 **잘 정리된 Markdown 카탈로그 + rg + 원문 부분 읽기 + 기존 테스트**다. 일부러 전체 문서를 매번 읽히는 방식만 대조군으로 삼지 않는다.

측정 항목:

- 알려진 관련 도구·파일을 찾은 비율과 필요한 추가 검색.
- 잘못된 원본·구성 선택, 미확인 부재 단정, 과거 검사 결과 재사용.
- tool 선택과 인자 검증 실패, schema 재조회·재시도 횟수.
- 기본 tool schema, 상세 schema, 요청·응답·후속 조회를 포함한 총 token.
- 기존 CLI 사용과 대비한 시간, index 갱신 비용, CPU·I/O, 유지 비용.
- 필수 검증 누락, stale 판정, 관측된 cache false hit.

작업 종류와 사용 모델·클라이언트 버전을 고정하거나 기록하고 반복 실험한다. 향상 폭은 측정 결과로 보고한다. Cached input의 과금 할인과 문맥에 포함되는 크기는 별도로 취급한다.

### 14.2 반드시 통과해야 할 회귀 시나리오

| 시나리오 | 요구 결과 |
|---|---|
| 빈 프로젝트·도구 0개 | 정상 빈 결과; 검색·기존 작업을 막지 않음 |
| 미등록 script·깨진 manifest | 각각 후보·해석 오류; 없는 도구로 단정하지 않음 |
| 같은 basename·생성 소스·백업 추정 | 후보와 역할 근거 분리; 자동 canonical 확정 없음 |
| CP949·UTF-8·혼합 newline | 선언·관찰·불확실성 구분; 손실 조용히 발생하지 않음 |
| 검색 제한·권한 실패·전수 count 미완료 | partial/lower_bound/unknown 구분 |
| 고정 snapshot 페이지 전부 조회 | baseline 결과와 동일, 중복·누락 없음 |
| 페이지 사이 원본·index 변경 | snapshot 유지 또는 명시적 재조회 오류 |
| 대형 scalar·누락 필드·숫자 ID | 질의 의미와 제한 보존, precision loss 검출/거부 |
| 필수 report 누락·테스트 0건·skipped | exit 0이어도 검증 완료 아님 |
| 테스트 후 관련 파일 변경 | 이전 검증 결과 stale |
| timeout·프로세스 crash·저장 재시작 | 부분 영수증 보존, 자동 성공 처리 없음 |
| plan 이후 입력·권한 변경 | 재계획·승인 재검사 또는 거부 |
| 공용 worktree 동시 수정 | 귀속 불명 표시, 다른 사용자 변경 자동 취소 없음 |
| 캐시 입력·설정·도구·출력 변조 | 재사용 거부 |
| Resource 미지원 클라이언트 | tool을 통한 동등 조회 가능 |

해당 범위의 회귀 테스트가 통과하는 것과 전체 저장소의 의미적 완전성은 구분한다. Search/adapter failure를 LLM이 무시하는지까지 end-to-end 사례로 점검한다.

## 15. 점검에 사용한 공식 자료

아래 자료는 일부 기술적 전제를 확인하기 위한 것이다. 이 설계 전체의 성능이나 정확성을 보증하는 근거는 아니다. MCP 호환성은 실제로 협상한 protocol/SDK 버전에 맞춰 검증한다.

- [MCP Resources](https://modelcontextprotocol.io/specification/2025-06-18/server/resources): Resource의 컨텍스트 반영은 application-driven이며 자동 토큰 절감을 규정하지 않는다.
- [MCP Tools](https://modelcontextprotocol.io/specification/2025-11-25/server/tools): tool schema·structured content 계약 및 호환성 표현을 확인할 기준.
- [Git diff](https://git-scm.com/docs/git-diff): rename/copy 탐지의 similarity threshold와 비용. Git 탐지를 절대적 계보로 다루지 않는 이유.
- [ripgrep Guide](https://github.com/BurntSushi/ripgrep/blob/master/GUIDE.md): 검색 대상·설정·출력과 정렬 옵션. 실제 검색 범위를 명시해야 하는 이유.
- [Bazel Hermeticity](https://bazel.build/basics/hermeticity): 호스트 도구·환경·시간 등 비밀폐 의존성이 재현성과 캐시에 영향을 준다는 기준.
