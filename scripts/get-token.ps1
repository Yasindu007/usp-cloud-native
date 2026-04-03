param(
  [string]$WorkspaceId = "ws_default",
  [string]$UserId = "usr_default",
  [string]$Scope = "read write"
)

$issuerUrl = if ($env:MOCK_ISSUER_URL) { $env:MOCK_ISSUER_URL } else { "http://localhost:9000" }

Write-Host "==> Requesting token from $issuerUrl"
Write-Host "    workspace_id: $WorkspaceId"
Write-Host "    user_id:      $UserId"
Write-Host "    scope:        $Scope"
Write-Host ""

$resp = curl.exe -s -w "`nHTTP_STATUS:%{http_code}`n" -X POST "$issuerUrl/token" `
  -H "Content-Type: application/x-www-form-urlencoded" `
  -d "grant_type=client_credentials" `
  -d "client_id=dev-client" `
  -d "client_secret=dev-secret" `
  -d "workspace_id=$WorkspaceId" `
  -d "user_id=$UserId" `
  -d "scope=$Scope"

$parts = $resp -split "HTTP_STATUS:"
$body = $parts[0].Trim()
$status = if ($parts.Length -gt 1) { $parts[1].Trim() } else { "000" }

try {
  $token = ($body | ConvertFrom-Json).access_token
} catch {
  $token = ""
}

if (-not $token) {
  Write-Host "Failed to get token. Response:"
  Write-Host "HTTP $status"
  Write-Host $body
  exit 1
}

Write-Host "Token:"
Write-Host $token
Write-Host ""
Write-Host "Use with curl:"
Write-Host "  curl.exe -H ""Authorization: Bearer $token"" `"
Write-Host "       -H ""Content-Type: application/json"" `"
Write-Host "       http://localhost:8080/api/v1/urls"
