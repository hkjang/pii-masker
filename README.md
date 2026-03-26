# PII Masker API

`mattermost-upstage-pii-plugin`의 핵심 서버 소스(`attachments`, `upstage`, `masking`, `execution`의 흐름)를 독립형 파일 마스킹 API로 재구성한 프로젝트입니다.

## 지원 기능

- `GET /`, `GET /ui`
  - 브라우저에서 바로 업로드 테스트 가능한 Playground 페이지 제공
- `POST /v1/mask`
  - `multipart/form-data` 업로드
  - `file` 필드로 `PDF` 또는 `PNG` 전송
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
- `PII_MASKER_UPSTAGE_BASE_URL`
- `PII_MASKER_UPSTAGE_AUTH_MODE`
- `PII_MASKER_UPSTAGE_AUTH_TOKEN`
- `PII_MASKER_ALLOW_HOSTS`
- `PII_MASKER_DEFAULT_TIMEOUT_SECONDS`
- `PII_MASKER_MAX_FILE_SIZE_MB`
- `PII_MASKER_MAX_PAGES`
- `PII_MASKER_DEFAULT_MODEL`
- `PII_MASKER_DEFAULT_LANG`
- `PII_MASKER_DEFAULT_SCHEMA`
- `PII_MASKER_DEFAULT_VERBOSE`
- `PII_MASKER_ENABLE_DEBUG`
- `PII_MASKER_ENABLE_EMBEDDED_UPSTAGE_MOCK`

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

브라우저 테스트 페이지는 `http://127.0.0.1:18080/`에서 확인할 수 있습니다.

```powershell
$env:PII_MASKER_BASE_URL="http://127.0.0.1:18080"
./scripts/smoke-test.ps1
```

## Docker 이미지 내보내기

```powershell
./scripts/export-image.ps1
```

기본값으로 `pii-masker:latest` 이미지를 `pii-masker-image.tar.gz`로 내보냅니다.
