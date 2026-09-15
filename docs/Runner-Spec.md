# RepoPlane Runner Specification

상태: Accepted 1.3

## 1. 범위와 권한

Runner는 catalog에 등록되고 `execution.trusted_for_run=true`인 capability만 실행한다.
임의 executable, argv 배열, shell 문자열을 MCP 입력으로 받지 않는다. 세 실행 tool은
안정적으로 노출하며 local stdio에서는 첫 사용 시 MCP elicitation으로 runtime 승인을
받는다. `--enable-runner`는 prompt 없는 host 사전 승인으로 남는다. HTTP에서는 기존 host
opt-in과 token scope를 모두 요구한다. 상세 lease 계약은
[Runtime-Access-Spec.md](Runtime-Access-Spec.md)를 따른다.

## 2. Prepare → Execute → Inspect

`run_prepare`는 capability ID/revision, typed arguments와 configuration을 받아 manifest를
새로 읽고 argument schema를 검증한다. 실제 argv/cwd, executable identity, 선언
input/output, preflight 결과, fan-out과 한도를 고정한 durable plan을 만든다.
optional `cache_mode=auto|bypass`를 받으며 cache policy, eligibility, key 존재 여부, hit/miss와
안정적인 reason을 같은 plan에 기록한다.

`run_execute`는 plan ID를 받아 manifest revision, executable identity와 선언 input만
재검사한다. 관련 없는 worktree 변경은 차단하지 않는다. plan은 한 번만 실행할 수 있고
실행은 background에서 진행된다. 변경 command는 자동 재시도하지 않는다.
검증된 cache hit이면 dependency와 qualification 및 blob hash를 다시 확인한 뒤 subprocess
없이 output을 materialize하고 state `reused`로 끝낸다. 복원 전 durable
`materializing` 상태를 기록하고 restart 시 성공으로 추측하지 않는다. miss나 cache 거절은 기존 background
실행 경로를 그대로 사용한다.

`run_inspect`는 `status`, `stdout`, `stderr`, `artifact`, `cancel` action을 제공한다.
stream과 retained artifact 조회는 offset/byte limit을 사용한다. artifact는 같은
workspace/run의 record ref만 허용하고 조회할 때 content hash를 다시 검증한다. cancel은
해당 run을 시작할 권한과 같은 host capability에 속한다.
reused run은 source run/artifact와 materialization 상태를 보여주며 실행되지 않은 stdout과
stderr를 `expired`로 명시한다.

## 3. 실행 호환성

argv element는 template literal 또는 `{argument}` 치환이며 shell 재해석을 거치지 않는다.
문자열, 정수, boolean argument를 지원하고 알 수 없는 argument는 거부한다. 실행 kind는
초기 버전에서 `cli`이며 플랫폼 adapter가 native executable과 PATH가 반환한 Windows
command wrapper 또는 POSIX shebang을 운영체제 규칙에 맞게 실행한다. Windows batch
adapter는 공백·Unicode와 quoted metacharacter 경계를 보존하며 quote, percent expansion,
개행처럼 안전하게 단일 argv로 보장할 수 없는 동적 값은 실행 전에 거부한다.

Catalog argument type `host_ref`는 명시적인 운영 host alias에만 사용한다. Runner는 값을
소문자로 정규화하고 current `memo.v2` host fact를 한 번의 bounded records 조회로 결합한다.
일치하는 사실은 기존 preflight `checks` 배열에 `informational` 결과로 role, OS, tier와
제한된 service 요약 및 record identity를 남긴다. 사실 부재, bounded partial scan, 같은
alias의 OS/role 충돌은 `unknown` check와 warning이며 실행 가능 여부를 바꾸지 않는다.
관리 path는 plan 요약에 복사하지 않는다. 임의 문자열 argument에서 host처럼 보이는 값을
추측하거나 AGENTS.md prose를 host inventory로 해석하지 않는다.

timeout과 cancel은 전체 process tree에 전파한다. Windows는 Windows 전용 process API,
Linux는 process group을 사용한다. 지원되지 않는 필수 process/isolation 조건만 hard
failure로 처리하고 CPU/memory/network 격리를 제공했다고 추측하지 않는다.

## 4. 완료 조건

- 현재 14개 typed tool의 이름·schema·안정적 노출이 유지된다.
- 승인되지 않은 Runner 호출은 실행 전에 input-required 상태가 된다.
- 기존 catalog manifest와 records DB가 migration 없이 계속 읽힌다.
- Windows/Linux에서 공백·Unicode, quoting, exit code, timeout, cancel, child cleanup을
  실제 subprocess test로 고정한다.
- prepare 이후 manifest/executable/input 변경과 중복 execute를 거부한다.
- 실패와 중단도 조회 가능한 receipt가 되며 MCP stdout은 protocol frame만 포함한다.
- cache는 [Cache-Spec.md](Cache-Spec.md)의 별도 opt-in과 qualification gate를 지키며 Runner
  세 tool 안에만 존재한다. 임의 shell과 network transport는 포함하지 않는다.
