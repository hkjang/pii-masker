# PII Masker API

`mattermost-upstage-pii-plugin`의 핵심 서버 소스(`attachments`, `upstage`, `masking`, `execution`의 흐름)를 독립형 파일 마스킹 API로 재구성한 프로젝트입니다.

## 지원 기능

- `GET /`, `GET /ui`
  - 브라우저에서 바로 업로드 테스트 가능한 Playground 페이지 제공
- `POST /v1/mask`
  - `multipart/form-data` 업로드
  - `file` 필드로 `PDF`, `PNG`, `JPG`, `JPEG` 전송
  - 응답은 `multipart/mixed`
  - 1번 파트: JSON 메타데이터
  - 2번 파트: 마스킹된 파일 바이너리
- `POST /v1/jobs`
  - 비동기 작업 생성
- `GET /v1/jobs/{job_id}`
  - 작업 상태 조회
- `GET /v1/jobs/{job_id}/result`
  - 결과 파일 다운로드
- `GET /v1/history`
  - 최근 작업 이력 조회
- `POST /v1/test-connection`
  - Upstage 호환 추론 엔드포인트 연결 점검
- `GET /v1/health`
  - 헬스체크
- `GET /v1/config/public`
  - 공개 설정 조회

업로드 요청(`POST /v1/mask`, `POST /v1/jobs`)의 본문은 `PII_MASKER_MAX_FILE_SIZE_MB`에 멀티파트 여유분 64KB를 더한 크기에서 잘립니다. 그보다 큰 본문은 끝까지 읽지 않고 `413`(`payload_too_large`)로 즉시 거절합니다.

이미지 업로드는 헤더에 적힌 해상도가 5천만 픽셀(50MP, 600dpi A4 스캔 이상)을 넘으면 픽셀 데이터를 디코딩하기 전에 `400`으로 거절합니다. 파일 크기는 작지만 거대한 해상도를 선언한 압축 폭탄이 디코딩 단계에서 메모리를 고갈시키는 것을 막기 위한 제한입니다.

비동기 작업(`POST /v1/jobs`)은 동시에 `PII_MASKER_MAX_CONCURRENT_JOBS`개(기본 4개)만 실행합니다. 초과분은 `queued` 상태로 대기하다가 순서대로 실행되며, 대기 중인 작업은 문서 바이트를 메모리에 들고 있지 않고 차례가 오면 저장된 입력 파일을 다시 읽습니다. 업로드를 한꺼번에 몰아넣어 서버 메모리를 고갈시키는 것을 막기 위한 제한입니다.

동기 요청(`POST /v1/mask`)도 같은 이유로 동시에 `PII_MASKER_MAX_CONCURRENT_SYNC`개(기본 4개)만 처리합니다. 슬롯이 모두 차 있으면 `PII_MASKER_SYNC_QUEUE_WAIT_SECONDS`초(기본 10초)까지 기다렸다가, 그래도 자리가 나지 않으면 문서를 건드리지 않고 `503`(`server_busy`, `Retry-After` 헤더 포함)로 돌려보냅니다. 대기 시간을 `0`으로 두면 기다리지 않고 즉시 거절합니다. 이 제한이 없으면 `/v1/mask`를 동시에 호출하는 것만으로 비동기 작업 제한을 우회해 문서 렌더링 메모리를 무제한으로 쓸 수 있습니다.

비동기 작업이 저장한 파일(업로드 원본과 마스킹 결과)은 마지막 상태 변경으로부터 `PII_MASKER_JOB_RETENTION_HOURS`시간(기본 24시간)이 지나면 job 디렉터리째 삭제되고 이력에서도 사라집니다. 마스킹해 달라고 받은 원본이 곧 개인정보이므로 무기한 보관하지 않기 위한 정책입니다. 서버 기동 직후와 그 뒤 주기적으로 정리하며, `0`을 주면 정리를 끄고 모든 파일을 남깁니다. 아직 `queued`/`running` 상태인 작업은 보존 기간과 무관하게 유지됩니다.

업로드 파일명은 경로 구분자, 제어문자(`CR`/`LF` 포함), 따옴표, 역슬래시를 제거하고 120바이트로 잘라서 사용합니다. 클라이언트는 파일명을 RFC 2231(`filename*=utf-8''...`)로 인코딩해 보낼 수 있어서, 디코딩된 이름에 개행이 섞이면 응답 `multipart` 파트 헤더나 추론 서버로 보내는 요청 헤더가 조작될 수 있기 때문입니다. 한글 등 비ASCII 파일명은 그대로 유지됩니다.

## 마스킹 규칙

다음 12개 기준을 구현했습니다.

1. 주민등록번호 뒤 7자리 마스킹
2. 운전면허번호 세 번째 묶음 6자리 마스킹
3. 여권번호 뒤 4자리 마스킹
4. 외국인등록번호 뒤 7자리 마스킹
5. 휴대폰번호 뒤 4자리 마스킹
6. 전화번호 뒤 4자리 마스킹
7. 신용카드번호 앞 12자리 마스킹
8. 계좌번호 마지막 묶음 제외 마스킹
9. 이름 짝수 자리 마스킹
10. 이메일 ID 앞 3자리 제외 마스킹
11. IP 주소 세 번째 옥텟 마스킹
12. 주소 하위 정보 마스킹

파일 마스킹은 Upstage류 응답의 `boundingBoxes` 좌표를 재사용하되, 필드 전체를 무조건 덮는 대신 `원문 -> 마스킹 문자열` 차이를 계산해서 실제로 숨겨야 하는 문자 비율만 덮습니다.

## 환경 변수

- `PII_MASKER_ADDR`
- `PII_MASKER_PUBLIC_BASE_URL`
- `PII_MASKER_STORAGE_DIR`
- `PII_MASKER_JOB_RETENTION_HOURS`
- `PII_MASKER_UPSTAGE_BASE_URL`
- `PII_MASKER_UPSTAGE_AUTH_MODE`
- `PII_MASKER_UPSTAGE_AUTH_TOKEN`
- `PII_MASKER_ALLOW_HOSTS`
- `PII_MASKER_DEFAULT_TIMEOUT_SECONDS`
- `PII_MASKER_MAX_FILE_SIZE_MB`
- `PII_MASKER_MAX_PAGES`
- `PII_MASKER_MAX_CONCURRENT_JOBS`
- `PII_MASKER_MAX_CONCURRENT_SYNC`
- `PII_MASKER_SYNC_QUEUE_WAIT_SECONDS`
- `PII_MASKER_DEFAULT_MODEL`
- `PII_MASKER_DEFAULT_LANG`
- `PII_MASKER_DEFAULT_SCHEMA`
- `PII_MASKER_DEFAULT_VERBOSE`
- `PII_MASKER_ENABLE_DEBUG`
- `PII_MASKER_ENABLE_EMBEDDED_UPSTAGE_MOCK`

`PII_MASKER_ALLOW_HOSTS`는 추론 요청을 보낼 수 있는 호스트 목록(쉼표 구분)입니다. 비워 두면 `PII_MASKER_UPSTAGE_BASE_URL`의 호스트만 허용합니다. 목록에 없는 호스트로 향하는 요청은 전송 전에 차단되며, 리다이렉트 응답도 같은 목록으로 검사하므로 업로드한 문서와 인증 토큰이 허용되지 않은 호스트로 따라가지 않습니다. 항목은 `api.upstage.ai`처럼 호스트만 적거나 `127.0.0.1:8080`처럼 포트까지 적을 수 있습니다.

## 로컬 실행

```powershell
$env:GOCACHE="$PWD\\.gocache"
$env:GOTMPDIR="$PWD\\.gotmp"
$env:GOSUMDB="off"
go run ./cmd/pii-masker
```

서버가 올라오면 브라우저에서 `http://127.0.0.1:8080/` 또는 `http://127.0.0.1:8080/ui`로 접속해 파일 업로드 테스트를 바로 할 수 있습니다.

## 테스트

```powershell
$env:GOCACHE="$PWD\\.gocache"
$env:GOTMPDIR="$PWD\\.gotmp"
$env:GOSUMDB="off"
go test ./...
```

## Docker

```powershell
docker compose up --build -d
```

기본 `docker-compose.yml`은 호스트 `18080` 포트로 노출되며, 임베디드 mock inference 엔드포인트를 켜 둔 상태로 올라오므로 외부 Upstage 서버 없이도 바로 API 테스트가 가능합니다.

브라우저 테스트 페이지는 `http://127.0.0.1:18080/`에서 확인할 수 있고, `PDF`, `PNG`, `JPG`, `JPEG` 업로드를 지원합니다.

```powershell
$env:PII_MASKER_BASE_URL="http://127.0.0.1:18080"
./scripts/smoke-test.ps1
```

## Docker 이미지 내보내기

```powershell
./scripts/export-image.ps1
```

기본값으로 `pii-masker:latest` 이미지를 `pii-masker-image.tar.gz`로 내보냅니다.

## tar.gz 이미지로 바로 실행

```sh
sh ./scripts/run-from-archive.sh ./pii-masker-image.tar.gz
```

기본값은 Docker named volume을 써서 권한 문제를 피합니다. 호스트 디렉터리에 직접 저장하고 싶으면 `DATA_DIR`를 지정하면 됩니다.

```sh
DATA_DIR=/srv/pii-masker-data sh ./scripts/run-from-archive.sh
```

필요하면 환경 변수로 포트와 컨테이너 이름도 바꿀 수 있습니다.

```sh
HOST_PORT=28080 CONTAINER_NAME=pii-masker-demo sh ./scripts/run-from-archive.sh
```

같은 이름의 컨테이너가 이미 있으면 기본적으로 중단하고, `FORCE_RECREATE=1`을 주면 기존 컨테이너를 지우고 다시 띄웁니다. 바인드 마운트를 쓸 때는 스크립트가 `jobs`, `logs` 디렉터리를 미리 만들고 쓰기 권한도 한 번 맞춰 줍니다.
