# PII Masker API

`mattermost-upstage-pii-plugin`의 핵심 서버 소스(`attachments`, `upstage`, `masking`, `execution`의 흐름)를 독립형 파일 마스킹 API로 재구성한 프로젝트입니다.

## 지원 기능

- `GET /`, `GET /ui`
  - 브라우저에서 바로 업로드 테스트 가능한 Playground 페이지 제공
  - 동기(`/v1/mask`)·비동기(`/v1/jobs`) 둘 다 실행할 수 있고, 검출 필드·마스킹 영역 수·적용 규칙·PII 요약 표와 원본/결과 미리보기를 보여 줍니다
  - 마스킹 영역이 0곳인 결과(원본이 그대로 반환된 경우)와 API 오류의 코드·상세를 눈에 띄게 표시하며, 서버 제한(크기·형식)에 맞지 않는 파일은 업로드 전에 걸러 냅니다
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
  - `HEAD`도 허용(본문 없이 크기·헤더만)
- `GET /v1/history`
  - 최근 작업 이력 조회 (`?limit=` 기본 20, 최대 100)
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

비동기 작업이 저장한 파일(업로드 원본과 마스킹 결과)은 마지막 상태 변경으로부터 `PII_MASKER_JOB_RETENTION_HOURS`시간(기본 24시간)이 지나면 job 디렉터리째 삭제되고 이력에서도 사라집니다. 마스킹해 달라고 받은 원본이 곧 개인정보이므로 무기한 보관하지 않기 위한 정책입니다. 서버 기동 직후와 그 뒤 주기적으로 정리하며, `0`을 주면 정리를 끄고 모든 파일을 남깁니다. 아직 `queued`/`running` 상태인 작업은 보존 기간과 무관하게 유지됩니다. 기록(`job.json`)이 없거나 손상됐거나 디렉터리 이름과 다른 작업을 가리키는 job 디렉터리는 API로 접근할 길이 없으므로 기동 시 바로 삭제합니다(업로드 저장과 기록 저장 사이에 프로세스가 죽거나 디스크가 가득 차면 생기는 상태입니다). 기록은 임시 파일에 쓴 뒤 교체하므로 상태 변경 도중 프로세스가 죽어도 직전 상태가 남습니다.

서버는 `SIGINT`/`SIGTERM`을 받으면 새 연결 수신을 멈추고 처리 중인 요청이 끝날 때까지 `PII_MASKER_SHUTDOWN_TIMEOUT_SECONDS`초(기본 45초)까지 기다린 뒤 종료합니다. 배포·재시작 중에 마스킹이 끝난 응답이 잘려 나가지 않게 하고, 보존 기간 정리 작업도 함께 멈추기 위해서입니다. 비동기 작업은 요청과 별개의 고루틴에서 돌기 때문에, 같은 제한 시간 안에서 이미 `running`인 작업이 끝나는 것까지 기다립니다. 아직 슬롯을 못 잡은 `queued` 작업은 기다리지 않고 `job_interrupted`(재시도 가능) 오류로 표시하며, 제한 시간 안에 끝나지 못한 `running` 작업은 파일을 그대로 둔 채 다음 기동 때 같은 오류로 표시됩니다. 이때 시각은 작업이 마지막으로 바뀐 시점 그대로 두고 기록해 두므로, 서버를 자주 재시작해도 남아 있는 업로드 원본의 보존 기한이 뒤로 밀리지 않습니다. 요청 헤더를 다 보내지 않는 연결은 `PII_MASKER_READ_HEADER_TIMEOUT_SECONDS`초(기본 15초)에, 요청 없이 열려만 있는 연결은 `PII_MASKER_IDLE_TIMEOUT_SECONDS`초(기본 60초)에 끊습니다. 큰 업로드나 느린 추론 응답을 중간에 끊지 않도록 본문 읽기·응답 쓰기에는 제한을 두지 않습니다.

문서나 그 메타데이터를 돌려주는 응답(`/v1/mask`, `/v1/jobs`, `/v1/jobs/{job_id}`, `/v1/jobs/{job_id}/result`, `/v1/history`)에는 `Cache-Control: no-store`와 `X-Content-Type-Options: nosniff`를 붙입니다. 캐시 지시자가 없는 `200 GET` 응답은 중간 프록시나 브라우저 디스크 캐시가 임의로 보관할 수 있어서, 보존 기간이 지나 서버에서 지운 마스킹 결과가 캐시에 남아 있을 수 있기 때문입니다. 문서를 담지 않는 `/v1/health`, `/v1/config/public`, UI 정적 파일에는 붙이지 않습니다.

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

파일 마스킹은 Upstage류 응답의 `boundingBoxes` 좌표를 재사용하되, 필드 전체를 무조건 덮는 대신 `원문 -> 마스킹 문자열` 차이를 계산해서 실제로 숨겨야 하는 문자 비율만 덮습니다. 문자 위치는 문서에 실제로 인쇄된 값(`value`, `rawValue`, `text`)을 기준으로 잡고, 길이가 달라질 수 있는 `refinedValue`/`normalizedValue`는 그 값이 없을 때만 씁니다. 한 값에 bounding box가 여러 개 붙어 있으면(줄바꿈된 주소, 여러 번 등장한 값) 어느 박스에 어떤 글자가 들었는지 알 수 없으므로 비율 계산 없이 박스 전체를 덮습니다.

좌표는 세 가지 방식을 모두 받습니다. 값이 전부 `0~1` 사이면 페이지 비율로 보고, 응답의 `metadata.pages[]`(또는 `pageSizes`)에 페이지 크기가 있으면 그 크기 기준 픽셀 좌표로 보고 문서 크기에 맞게 스케일하며, 둘 다 아니면 문서 자체 단위(PDF는 포인트, 이미지는 픽셀)로 봅니다.

### 마스킹 실패 처리 (fail-closed)

추론 엔드포인트가 `200`을 돌려줘도 아래 경우에는 결과 파일을 내보내지 않고 실패로 처리합니다. 마스킹이 안 된 파일이 `completed` 상태로 다운로드되는 것을 막기 위해서입니다.

- 응답 본문을 JSON으로 해석하지 못했을 때 (`upstream_payload_unrecognized`)
- 응답에 `fields`/`document`/`documents`/`groups`/`entities` 같은 필드 목록이 아예 없거나, 좌표는 있는데 값을 하나도 읽어내지 못했을 때 (`upstream_payload_unrecognized`)
- PII 필드는 있는데 bounding box가 없을 때 (`processing_failed`)
- 영역이 문서에 없는 페이지를 가리키거나, 페이지 밖에 떨어지거나, 페이지 크기 정보 없이 페이지보다 큰 좌표를 쓰고 있어서 어디를 덮어야 할지 알 수 없을 때 (`processing_failed`)

동기 요청은 이때 `502`(`processing_failed`는 `400`)를 돌려주고, 비동기 작업은 `failed` 상태에 `download_url` 없이 남습니다. 응답 파싱에는 잘리지 않은 본문 전체를 쓰며, 디버그용으로 `16KB`에서 잘라 보관하는 복사본은 표시에만 씁니다.

응답 메타데이터의 `mask_policy.applied_regions`는 실제로 문서 위에 그린 박스 수입니다. `completed`인데 `0`이면 엔드포인트가 PII를 하나도 보고하지 않아 업로드한 파일이 그대로 돌아온 것입니다.

PDF 마스킹은 페이지 위에 검은 사각형을 덧그리는 방식이라 화면과 인쇄에서는 가려지지만, 텍스트 레이어에 있던 원문은 PDF 안에 그대로 남습니다. 텍스트 복사·검색까지 막아야 하면 PDF를 이미지로 변환한 뒤 마스킹하세요.

## 환경 변수

- `PII_MASKER_ADDR`
- `PII_MASKER_PUBLIC_BASE_URL`
- `PII_MASKER_STORAGE_DIR`
- `PII_MASKER_READ_HEADER_TIMEOUT_SECONDS`
- `PII_MASKER_IDLE_TIMEOUT_SECONDS`
- `PII_MASKER_SHUTDOWN_TIMEOUT_SECONDS`
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
