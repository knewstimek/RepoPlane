# RepoPlane Search Adapters Specification

이 문서의 11-tool 수치는 1.0 기준선이다. runtime read grant로 추가된 12번째 control
tool과 외부 read scope는 [Runtime Access 명세](Runtime-Access-Spec.md)를 따른다.

상태: Accepted 1.0

## 1. 범위

Stage 7은 새 MCP tool을 추가하지 않고 `catalog_query`, `workspace_search`, `data_query`의
adapter를 확장한다. 기존 filename/exact/regex/text-range/JSONL 계약과 cursor는 그대로
유효하다. 추가 범위는 Git history, 기존 symbol index, Markdown frontmatter, 일반 JSON,
log, CSV와 TSV다. semantic search와 범용 언어 parser는 포함하지 않는다.

각 adapter는 결과의 `basis`와 engine/version을 공개한다. working tree, immutable Git
revision, mutable/generated symbol index를 하나의 근거로 합치거나 한 채널의 점수로 다른
채널의 정확 일치를 숨기지 않는다. adapter 부재는 정상 빈 결과가 아니라 `unsupported`,
제한·shallow history·읽기 실패는 `partial`과 `lower_bound|unknown`으로 표현한다.

## 2. Git history

`workspace_search(mode=git_history)`는 고정 revision 범위에서 commit metadata, changed path,
또는 patch text를 검색한다. 요청은 pattern, exact/regex match kind, revision, optional
since/until과 기존 include/exclude scope를 사용한다. 기본 revision은 현재 `HEAD`이며 시작
시점에 full object ID로 고정한다.

- Git 실행은 argument array와 고정된 비대화형 옵션을 사용하고 hooks, pager, textconv,
  external diff를 실행하지 않는다.
- 결과는 commit object ID, workspace-relative path, line/range가 확인될 때의 위치,
  `git:` source ref와 match channel을 제공한다. author name/email과 commit body는 명시적
  projection 없이 기본 결과에 싣지 않는다.
- revision 부재, ambiguous revision, shallow boundary, missing object와 deadline을 구분한다.
- 고정 result set을 page화하므로 후속 page가 변한 HEAD와 섞이지 않는다.
- 검색 범위 전체를 확인했을 때만 count를 `exact`로 표시한다.

## 3. Symbol index

`workspace_search(mode=symbol)`은 host가 명시한 기존 index만 읽는다. RepoPlane이 모든 언어를
추론하거나 source를 자체 parsing하지 않는다. 지원 입력은 `symbol-index.v1` JSONL과
Universal Ctags JSON Lines의 안전한 부분집합이다.

각 entry는 name, kind, language, workspace-relative path, line/range와 source identity를
가진다. 요청은 pattern과 optional symbol kind/language를 사용한다. exact name을 prefix와
substring보다 먼저 두되 모든 결과는 channel을 유지한다. index source hash와 선언된 source
revision이 현재 workspace와 일치할 때만 `current`; 증명할 수 없으면 `unknown`, 명시적
불일치는 `stale`이다. stale 결과는 반환할 수 있지만 경고와 validity를 제거하지 않는다.
index가 없거나 dialect가 미지원이면 `unsupported`다.

## 4. Markdown frontmatter

catalog root의 Markdown은 파일 시작의 UTF-8 YAML frontmatter가 RepoPlane catalog 필드를
가질 때 catalog 원본이 될 수 있다. YAML/JSON manifest와 같은 ID/revision validation을
사용하며 문서 본문은 실행 명령으로 해석하지 않는다. alias 폭탄, custom tag, duplicate key,
다중 document와 크기 한도를 거부한다. 한 파일의 오류는 issue로 공개하고 이전의 완전한
catalog generation을 부분 generation으로 교체하지 않는다.

frontmatter-only 문서 항목은 정상이다. 실행은 기존 trusted host policy와 strict execution
schema를 모두 만족할 때만 가능하다.

## 5. Structured data와 log

`data_query`는 다음 mode/dialect를 추가한다.

- `json` + `json-pointer`: RFC 6901 selection 뒤 기존 top-level equality filter와 field
  projection을 적용한다. 숫자는 `json.Number`로 보존하며 IEEE-754 변환을 하지 않는다.
- `delimited` + `csv|tsv`: Go `encoding/csv` 규칙, 첫 record header, exact typed-as-text
  equality filter와 field projection을 사용한다. duplicate/empty header는 거부한다.
- `log` + `exact|regex`: 선택 encoding으로 line 단위 검색한다. 일치 위치는 고정 result set을
  page화하며 기본 응답에 거대한 location 배열을 복제하지 않는다.

처리 순서는 decode/parse, selection, filter, projection, stable source order, pagination이다.
malformed `fail|skip_with_warning`, 누락 필드는 non-match, empty string과 missing은 다르다.
단일 거대 item, 입력 한도, deadline과 malformed skip은 partial 상태를 보존한다. 임의 SQL,
shell, eval, 외부 schema URL과 custom function은 실행하지 않는다.

## 6. 예산, freshness, privacy

모든 adapter는 공통 item/byte/time limit와 최대 scan limit를 동시에 적용한다. 원문은 source
ref로 확장하고 summary/items/evidence에 반복하지 않는다. mutable source는 hash를 cursor와
result set에 묶고 후속 원문 조회에서 재검사한다. immutable Git object는 object ID를 ref에
포함한다.

MCP tool 수는 11개를 유지한다. compact `tools/list` schema는 의미·기본값·상태 필드를
삭제하지 않은 채 32 KiB 미만이어야 한다. 경로, Git identity, log와 frontmatter에서 개인
identity나 secret을 새로 추론하거나 기본 projection에 추가하지 않는다.

## 7. 완료 gate

- 각 adapter의 complete/partial/unsupported/stale와 pagination 회귀 test
- baseline CLI/parser 결과와 고정 snapshot의 page 합집합이 동일함
- Git shallow/missing revision, symbol absent/stale, malformed frontmatter/data 회귀 test
- JSON 큰 정수, CSV quoted newline, CP949/EUC-KR log와 거대 line 회귀 test
- adapter 실패가 기존 검색과 catalog generation을 손상하지 않음
- schema 32 KiB, tool 11개, 공개 privacy와 Windows/Linux verification 통과
