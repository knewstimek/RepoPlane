# RepoPlane Runner Specification

상태: Accepted 1.1

## 1. 범위와 권한

Runner는 catalog에 등록되고 `execution.trusted_for_run=true`인 capability만 실행한다.
임의 executable, argv 배열, shell 문자열을 MCP 입력으로 받지 않는다. 실행 계층은
`--enable-runner`를 명시한 host에서만 `run_prepare`, `run_execute`, `run_inspect` 세
tool을 노출한다. 이 host opt-in과 MCP client의 tool 승인이 실행 권한이며 서버 내부의
중복 승인 token은 요구하지 않는다.

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

timeout과 cancel은 전체 process tree에 전파한다. Windows는 Windows 전용 process API,
Linux는 process group을 사용한다. 지원되지 않는 필수 process/isolation 조건만 hard
failure로 처리하고 CPU/memory/network 격리를 제공했다고 추측하지 않는다.

## 4. 완료 조건

- 기존 다섯 조회 tool과 opt-in record writer의 이름·schema·기본 노출이 유지된다.
- Runner를 켰을 때만 정확히 세 실행 tool이 추가된다.
- 기존 catalog manifest와 records DB가 migration 없이 계속 읽힌다.
- Windows/Linux에서 공백·Unicode, quoting, exit code, timeout, cancel, child cleanup을
  실제 subprocess test로 고정한다.
- prepare 이후 manifest/executable/input 변경과 중복 execute를 거부한다.
- 실패와 중단도 조회 가능한 receipt가 되며 MCP stdout은 protocol frame만 포함한다.
- cache는 [Cache-Spec.md](Cache-Spec.md)의 별도 opt-in과 qualification gate를 지키며 Runner
  세 tool 안에만 존재한다. 임의 shell과 network transport는 포함하지 않는다.
