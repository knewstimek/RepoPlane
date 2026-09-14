# RepoPlane Portable Memory Backup Specification

상태: Accepted 1.1  
대상: durable record와 보존된 Runner evidence의 machine-reset 복구

## 1. 목적

`durable` record는 프로세스 재시작에는 살아남지만 로컬 state directory를 잃으면 사라진다.
`memory_backup`은 RepoPlane MCP를 재시작하지 않고 이 비재생성 상태를 하나의 versioned ZIP으로
내보낸다. `repoplane memory restore`는 새 state directory에 이를 복원하고 현재 workspace
identity로 record ownership을 재바인딩한다.

## 2. 아카이브 계약

- 포맷 ID는 `repoplane-memory`, version은 `1`이다.
- `manifest.json`, `records.json`, `files/runs/**`, `files/artifacts/**`만 허용한다.
- record current row, 모든 revision, report-import receipt를 보존한다.
- Runner stream과 content-addressed artifact의 regular file만 보존한다.
- catalog/index/result set/cache entry는 재생성 가능하므로 제외한다.
- cursor/cache/audit key, `audit.db`, HTTP profile, token, Codex TOML은 포함하지 않는다.
- 응답은 archive 이름·크기·SHA-256·개수만 반환하고 record payload를 되풀이하지 않는다.

기본 expanded byte limit은 1 GiB, 최대는 4 GiB이고 entry/record 계열 합계는 각각 최대
100,000개다. 제한을 넘으면 partial archive를 성공으로 반환하지 않는다.

## 3. Runtime export

local stdio의 `memory_backup`은 존재하는 절대 destination directory를 받는다. workspace나
state directory 내부는 거부한다. 첫 호출은 MCP elicitation으로 exact canonical destination을
승인받고 같은 호출을 재개한다. 승인하지 않은 sibling, symlink retarget, HTTP runtime grant는
fail closed다. 활성 Runner process가 있으면 `runner_busy`로 거부하고 새 실행도 snapshot 동안
시작하지 않는다.

CLI의 `repoplane memory export --destination PATH`는 사용자가 직접 실행한 명시적 host
operation이므로 elicitation 없이 같은 포맷을 만든다.

## 4. Restore

복원은 MCP server가 시작되기 전의 one-shot CLI operation이다.

```text
repoplane memory restore --archive ARCHIVE --workspace WORKSPACE --state-dir STATE_DIRECTORY
```

ZIP-slip, symlink, duplicate/unknown entry, expanded-size 초과, manifest/count 불일치, 깨진 JSON,
불완전 revision history를 거부한다. 대상 workspace identity에 durable record가 있거나 대상
Runner file directory가 비어 있지 않으면 덮어쓰지 않는다. 성공 시 source absolute path와
무관하게 현재 workspace ID로 record와 import receipt를 원자적으로 재바인딩한다.

## 5. 호스트 설정과 비밀

이 아카이브는 평문일 수 있으므로 record·stream·artifact 자체도 민감정보로 취급하고 암호화된
외부 저장소에 보관해야 한다. Codex MCP 등록은 `codex mcp add repoplane -- repoplane`으로
재구성한다. HTTP profile과 그 profile이 참조하는 secret은 password manager 같은 별도 암호화
저장소에서 복원한다. RepoPlane이 host config와 secret을 임의 검색하거나 backup에 자동 포함하지
않는다.

## 6. 검증 조건

- 다른 absolute workspace path로 export/restore한 record가 전체 revision과 함께 조회된다.
- retained stream/artifact byte가 동일하게 복원되고 key/profile/cache는 archive에 없다.
- non-empty target, unsafe destination/entry, oversized archive와 active Runner가 fail closed다.
- MCP export는 one-time approval을 검증하고 compact receipt만 반환한다.
