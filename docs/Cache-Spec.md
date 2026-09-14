# RepoPlane Conservative Cache Specification

상태: Accepted 1.0

## 1. 범위와 기본값

Cache는 Runner가 이미 검증한 등록 capability의 결과만 재사용한다. 새 MCP tool을 추가하지
않고 `run_prepare`, `run_execute`, `run_inspect`에 cache 결정을 포함한다. host가
`--enable-cache`를 지정하지 않으면 cache는 완전히 비활성화되며, manifest 기본값도
`cache_policy: disabled`다.

지원 policy는 다음과 같다.

- `disabled`: lookup, 관찰, 저장을 하지 않는다.
- `observe`: 항상 프로세스를 실행하고 같은 key의 결과가 같은지 관찰한다.
- `verified`: 현재 qualification evidence가 있을 때만 적격 entry를 재사용한다.

cache 거절, miss, stale, corrupt, qualification 부재는 등록 실행 자체를 막지 않는다.
prepare 응답에 이유를 남기고 정상 Runner 실행으로 진행한다. 요청의
`cache_mode: bypass`는 lookup과 materialization만 건너뛰되 결과 관찰은 유지하여 cache
on/off 비교를 가능하게 한다.

## 2. Manifest 계약

`observe`와 `verified`는 다음 선언을 요구한다.

```yaml
cache_policy: observe # disabled | observe | verified
cache:
  contract_revision: 1
  output_contract: schema-output.v1
  key_checks: [runtime.version]
  qualification_checks: [cache.schema-output.differential]
  restore_policy: missing_or_matching
  assumptions:
    inputs_complete: true
    outputs_complete: true
    external_state: none
    nondeterminism: none
    side_effects: declared_outputs_only
```

cache 가능한 capability는 `trusted_for_run: true`, `artifact_mode: capture`, 비어 있지 않은
inputs/outputs, versioned output contract를 가져야 한다. `key_checks`는 required executable
preflight check를 가리키며 identity가 key에 포함된다. `verified`는 모든
`qualification_checks`에 대해 같은 capability/configuration에 적용되는 current/passed
verification evidence를 요구한다.

assumption은 낙관적 추론이 아니라 작성자의 명시적 계약이다. 선언되지 않은 input,
동적 파일 발견, 시간·난수·네트워크·사용자 상태 의존, 선언 output 밖의 side effect가
있으면 cache를 켜지 않는다. 비밀일 수 있는 argument는 cache 대상 manifest에서 허용하지
않으며 key나 record에 원문을 저장하지 않는다.

## 3. Key와 privacy

key의 논리 입력은 다음 순서 고정 tuple이다.

1. key format와 cache/output contract revision
2. capability ID, manifest revision/source fingerprint, execution fingerprint
3. executable identity와 선언된 key-check identity
4. 원래 순서의 argv element와 configuration
5. 정렬된 선언 input path/content hash

각 값은 길이 prefix를 붙여 모호한 결합을 막고, host local `cache.key`로 HMAC-SHA256한다.
argv 순서, element 경계, 의미 있는 공백과 case를 보존한다. 전체 Git commit, 무관한 dirty
state, 절대 경로, 환경변수 값은 key에 넣지 않는다. 안전한 identity를 만들 수 없으면
cache miss로 처리한다.

## 4. Prepare, execute, inspect

`run_prepare`는 execution plan과 함께 `cache` 결정을 반환한다. 결정에는 policy/mode,
eligibility, key 유무, `hit|miss|bypassed|rejected|quarantined` 상태, 안정적인 reason code,
source run/evidence ref가 포함된다. prepare 뒤 manifest, executable, inputs, key-check
identity 또는 qualification이 바뀌면 execute는 `plan_stale`을 반환한다.

적격 hit의 `run_execute`는 subprocess를 만들지 않고 artifact blob을 검증한 뒤 output을
materialize한다. 새로 생성하는 durable `run-receipt.v2`는 cache-disabled 실행도 명시하며,
state `materializing`과 `reused`, source run, source artifact,
materialization 결과를 기록한다. 이는 프로세스가 실행됐다는 뜻이 아니다.
`run_inspect(status)`는 같은 cache 정보를 그대로 보여준다. reused run의 stdout/stderr는
`expired`로 명시한다.

miss나 bypass 실행이 성공하고 output 관찰과 artifact capture가 완전하면 entry 후보를
원자적으로 publish한다. 같은 key의 기존 결과와 동일하면 observation count를 늘리고,
다르면 entry를 `quarantined`로 바꾸며 어느 결과도 재사용하지 않는다. observe 횟수만으로
`verified`가 되지는 않는다.

## 5. Materialization

기본 `missing_or_matching` 정책은 output이 없으면 sibling temporary file을 검증 후 rename
하고, 이미 같은 hash면 no-op한다. 다른 content가 있으면 충돌로 거절하며 덮어쓰지 않는다.
부분 materialization 실패 시 이번에 새로 만든 파일만 best-effort rollback하고 receipt에
partial을 남긴다.

`replace_isolated_root`는 manifest가 단일 workspace-relative directory를 완전히 소유한다고
선언하고 모든 output이 그 아래에 있으며 input/tracked path와 겹치지 않을 때만 허용한다.
전체 새 tree를 sibling staging directory에서 검증한 뒤 directory rename으로 교체한다.
Windows에서도 기존 root는 `old_repoplane_cache_<timestamp>` sibling으로 먼저 회전하며,
복구 표식을 통해 중단된 교체를 다음 시작 때 정리한다. 이 증명을 할 수 없으면
`missing_or_matching`으로 자동 완화하지 않고 cache를 거절한다.

## 6. 저장, pin, 보존

output byte는 기존 content-addressed artifact blob store를 재사용한다. cache index는
재생성 가능한 `repoplane.db`의 additive migration이며 durable `records.db`와 분리한다.
entry는 key, 상태, source run/artifact refs, output path/hash/size, qualification refs,
관찰·hit 시각과 count만 저장한다.

active entry가 참조하는 hash는 pin으로 간주되어 Runner retention이 blob을 삭제하지 않는다.
기본 entry TTL은 30일이며 한 maintenance pass에서 최대 64개만 만진다. 크기 기반 eviction은
전체 pin scan과 host별 예산 계약을 함께 도입하기 전에는 사용하지 않는다. index가 사라지면 cache miss가 될 뿐 durable receipt는
손상되지 않는다. 누락·hash 불일치 blob은 hit를 거절하고 entry를 격리한다.

## 7. False-hit와 동시성 gate

- qualification이 stale/unknown/skipped/failed이면 cache만 거절한다.
- 동일 key에 서로 다른 output set이 관찰되면 원인을 수정하고 contract revision을 올린 뒤
  다시 qualification하기 전까지 격리를 해제하지 않는다.
- concurrent miss는 허용한다. 동일 결과는 한 entry로 수렴하고 다른 결과는 격리한다.
- lookup 뒤 모든 dependency와 blob hash를 재검증한다.
- `validity: current`는 `cache_hit`과 같은 뜻이 아니다.

필수 회귀 test는 cache on/off byte 비교, input/config/argv 순서/runtime identity mutation,
누락·변조 blob, output 충돌, qualification stale/unknown, 동시 동일/불일치 publish,
restart와 bounded eviction, Windows/Linux materialization을 포함한다.
