# RepoPlane Release Notes Specification

상태: Accepted 1.1

## 1. 목적

각 GitHub Release가 사용자에게 필요한 변화, 설치 방법, 호환성, 안전 경계와 검증 근거를
짧고 재현 가능하게 전달하도록 한다. Git history를 그대로 나열하거나 확인하지 않은 품질
표현을 사용하지 않는다.

## 2. 버전과 source

- tag는 `vMAJOR.MINOR.PATCH` SemVer 형식의 annotated tag다.
- 게시 제목은 `RepoPlane vMAJOR.MINOR.PATCH`다.
- release note 원본은 `docs/releases/vMAJOR.MINOR.PATCH.md`에 추적한다.
- `CHANGELOG.md`의 `Unreleased`를 같은 날짜의 version heading으로 동결하고 새 빈
  `Unreleased`를 연다.
- release는 tag가 가리키는 commit의 추적된 note만 사용한다. 게시 UI에서만 내용을 추가해
  source와 release를 갈라놓지 않는다.

## 3. 게시 형식

note는 다음 순서를 사용한다.

1. 한 문단 요약: 누구의 어떤 문제를 해결하는지
2. `Highlights`: 3–6개의 사용자 결과
3. `What ships`: 공개 tool과 중요한 opt-in surface
4. `Safety model`: 기본 비활성 기능과 보안 경계
5. `Install or upgrade`: archive, checksum, host dependency, restart 방법
6. `Compatibility`: 지원 OS/architecture, state/schema migration과 breaking change
7. `Verification`: tag commit과 통과한 required CI job
8. `Known limits`: unsupported/partial을 숨기지 않는 실제 제한
9. full changelog 링크

빈 section이나 commit-by-commit 목록은 넣지 않는다. `production-ready`, `secure`, `lossless`,
`complete` 같은 표현은 명시된 범위와 실행 증거가 없으면 쓰지 않는다.

## 4. Artifact 계약

v1.0 계열 기본 asset은 다음과 같다.

```text
repoplane_VERSION_windows_amd64.zip
repoplane_VERSION_linux_amd64.tar.gz
SHA256SUMS.txt
```

archive에는 version을 주입해 `-trimpath`로 빌드한 `repoplane.exe` 또는 `repoplane` 하나만
둔다. 현재 CI가 실행하지 않는 architecture를 단순 cross-compile 성공만으로 지원한다고
표시하지 않는다. checksum 파일은 lowercase SHA-256, 두 칸, 정확한 asset filename 순이며
release에 올린 byte를 대상으로 다시 검증한다.

## 5. 릴리스 gate

- clean worktree의 registered preflight, verify, public-release-check 통과
- tag 대상 commit의 Windows verify, Ubuntu verify와 Linux race 성공
- public tree와 reachable history privacy scan 통과
- fresh public checkout build/test 통과 또는 동일 commit의 clean CI checkout 증거
- archive 내부 filename, target format, embedded version 설정과 checksum 검증
- GitHub Release가 draft/prerelease가 아니고 tag와 asset 집합이 정확함

실패한 gate는 release note로 면제하지 않는다. 수정 commit의 CI가 녹색이 된 후 새 tag를
만들며 이미 공개한 tag를 강제로 이동하지 않는다.

## 6. RepoPlane MCP 사용

`release.status` catalog capability는 현재 `gh release list`가 지원하는 고정 JSON field만
사용해 원격 release 상태를 조회한다. field를 추측해 직접 호출하지 않는다. Release 생성,
tag push와 asset 업로드는 외부 변경이므로 capability 조회 권한이나 schema handle만으로
승인된 것으로 간주하지 않으며 사용자의 명시적 릴리스 의도 아래 수행한다.

## 7. 자동 게시

`.github/workflows/release.yml`은 `main`의 특정 commit에 대해 성공한 `CI` run을 재사용하고,
선언된 두 platform archive의 build, checksum, annotated tag, GitHub Release와 게시 후 byte
검증을 한 번의 guarded workflow로 수행한다. `CI`의 branch filter는 tag push에서 같은 test를
다시 실행하지 않는다. `main` push CI에는 complete reachable-history 공개 검사가 포함된다.

수동 dispatch는 `version`과 선택적인 `notes_file`을 받는다. `notes_file`을 생략하면
`docs/releases/vVERSION.md`를 사용하며, 현재 계약에서는 다른 경로를 거부한다. note는 이전
릴리스와 같은 필수 section 순서와 changelog 링크를 가져야 한다. 따라서 본문을 shell/JSON
인자로 복사하지 않고 추적된 원본만 게시한다.

```text
gh workflow run release.yml --ref main -f version=1.0.4 \
  -f notes_file=docs/releases/v1.0.4.md -f publish=true
```

RepoPlane Runner에서는 `release.dispatch`에 같은 두 typed argument를 전달할 수 있다. workflow
dispatch 자체가 외부 변경 승인이며, workflow는 tag가 이미 같은 commit을 가리키는 경우
재사용하고 게시 asset과 note를 검증된 결과로 복구할 수 있다.
GitHub UI의 `publish` 기본값은 `false`여서 실제 tag나 release 없이 동일 build를 점검할 수
있고, `release.dispatch`는 사용자의 Runner 승인을 받은 뒤 `publish=true`로 dispatch한다.
